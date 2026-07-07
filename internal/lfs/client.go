package lfs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

const (
	// MediaType is the LFS API content type for batch requests and responses.
	MediaType = "application/vnd.git-lfs+json"
	// maxBatch bounds objects per batch request (matches git-lfs).
	maxBatch = 100
)

// Object names one LFS object by content: its sha256 oid (lower hex) and
// size in bytes.
type Object struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// ObjectError is a per-object failure reported by the batch API — most
// commonly a 404 for a referenced oid nobody uploaded. Upload and Download
// aggregate them with errors.Join so a caller can name every failed oid.
type ObjectError struct {
	OID     string
	Code    int
	Message string
}

func (e *ObjectError) Error() string {
	return fmt.Sprintf("object %s: server: %d %s", e.OID, e.Code, e.Message)
}

// Credentials resolves and records Basic-auth credentials for the batch
// endpoint. gitcmd.Git implements it with git's credential machinery.
type Credentials interface {
	CredentialFill(ctx context.Context, rawurl string) (gitcmd.Credential, error)
	CredentialApprove(ctx context.Context, rawurl string, cred gitcmd.Credential) error
	CredentialReject(ctx context.Context, rawurl string, cred gitcmd.Credential) error
}

// Client speaks the LFS batch and basic-transfer protocols against one
// endpoint. Header carries auth headers applied to every batch request — the
// git-lfs-authenticate grant on ssh endpoints, or the endpoint's
// http.<url>.extraheader config on https endpoints; Creds, when set, supplies
// Basic auth after a batch 401. Content GET/PUT/verify requests carry only
// their action's own headers — hrefs are typically pre-signed to other
// hosts, and API auth must never leak to them. Not safe for concurrent use.
type Client struct {
	Endpoint string
	Header   map[string]string
	Creds    Credentials
	HTTP     *http.Client

	auth string // Basic Authorization value cached after a successful fill
}

// NewClient builds a client for ep authenticated for operation ("upload" or
// "download"). An ssh endpoint execs git-lfs-authenticate up front and the
// grant's href replaces the derived endpoint — the server, not our
// derivation, knows where its LFS API lives; an https endpoint sends any
// matching http.<url>.extraheader from the first batch request and falls back
// to git credential on a batch 401.
func NewClient(ctx context.Context, g gitcmd.Git, ep Endpoint, operation string) (*Client, error) {
	if ep.SSHUserHost == "" {
		header, err := extraHeader(ctx, g, ep.Href)
		if err != nil {
			return nil, err
		}
		return &Client{Endpoint: ep.Href, Header: header, Creds: g}, nil
	}
	grant, err := sshAuthenticate(ctx, ep, operation)
	if err != nil {
		return nil, err
	}
	if grant.Href == "" {
		return nil, fmt.Errorf("ssh %s git-lfs-authenticate: grant has no href", ep.SSHUserHost)
	}
	return &Client{Endpoint: strings.TrimSuffix(grant.Href, "/"), Header: grant.Header}, nil
}

type batchRequest struct {
	Operation string   `json:"operation"`
	Transfers []string `json:"transfers,omitempty"`
	Objects   []Object `json:"objects"`
	HashAlgo  string   `json:"hash_algo,omitempty"`
}

type batchResponse struct {
	Transfer string        `json:"transfer,omitempty"`
	Objects  []batchObject `json:"objects"`
	HashAlgo string        `json:"hash_algo,omitempty"`
}

type batchObject struct {
	OID     string            `json:"oid"`
	Size    int64             `json:"size"`
	Actions map[string]action `json:"actions,omitempty"`
	Error   *objectError      `json:"error,omitempty"`
}

// action is one transfer action: an href plus headers sent verbatim. The
// same shape is the ssh git-lfs-authenticate grant.
type action struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header,omitempty"`
}

type objectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type apiError struct {
	Message string `json:"message"`
}

// batchHTTP is the default client for batch API requests. Batch requests carry
// credentials, so it refuses any cross-scheme or cross-host redirect: net/http
// strips only the six canonical auth headers on a cross-origin redirect, which
// would replay a custom-named extraheader or an ssh grant header verbatim to
// the redirect target.
var batchHTTP = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("batch redirect to %s: refusing cross-origin redirect with credentials", req.URL)
	}
	return nil
}}

// httpClient is the client for content transfers (GET/PUT/verify), which carry
// only their action's own headers: c.HTTP when set, else the default client,
// which follows the cross-host redirects that pre-signed CDN hrefs rely on.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// batchClient is the client for the credential-carrying batch API: c.HTTP when
// set, else batchHTTP, which refuses the cross-origin redirects that would leak
// the batch credential.
func (c *Client) batchClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return batchHTTP
}

// batch posts operation ("upload" or "download") for objects in
// maxBatch-sized chunks and returns the server's per-object answers.
func (c *Client) batch(ctx context.Context, operation string, objects []Object) ([]batchObject, error) {
	var out []batchObject
	for chunk := range slices.Chunk(objects, maxBatch) {
		res, err := c.batchOne(ctx, operation, chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, res...)
	}
	return out, nil
}

func (c *Client) batchOne(ctx context.Context, operation string, objects []Object) ([]batchObject, error) {
	body, err := json.Marshal(batchRequest{Operation: operation, Transfers: []string{"basic"}, Objects: objects, HashAlgo: "sha256"})
	if err != nil {
		return nil, fmt.Errorf("batch %s: %w", operation, err)
	}
	res, err := c.post(ctx, body, c.auth)
	if err != nil {
		return nil, fmt.Errorf("batch %s: %w", operation, err)
	}
	if res.StatusCode == http.StatusUnauthorized && c.Creds != nil && c.auth == "" {
		_ = res.Body.Close() // drop the challenge body; the retry replaces res
		res, err = c.retryWithCredential(ctx, body)
		if err != nil {
			return nil, fmt.Errorf("batch %s: %w", operation, err)
		}
	}
	defer func() { _ = res.Body.Close() }()
	return decodeBatch(c.Endpoint, operation, res)
}

func (c *Client) post(ctx context.Context, body []byte, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/objects/batch", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", MediaType)
	req.Header.Set("Content-Type", MediaType)
	for k, v := range c.Header {
		req.Header.Set(k, v)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return c.batchClient().Do(req)
}

// retryWithCredential re-sends the batch once with Basic auth from git's
// credential machinery: approve on acceptance so helpers cache the
// credential, reject on a second 401 (the filled credential is bad) so they
// purge it. A 403 is authentication succeeding but authorization failing —
// the credential is kept, not erased, and the authorization failure is
// reported.
func (c *Client) retryWithCredential(ctx context.Context, body []byte) (*http.Response, error) {
	cred, err := c.Creds.CredentialFill(ctx, c.Endpoint)
	if err != nil {
		return nil, err
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(cred.Username+":"+cred.Password))
	res, err := c.post(ctx, body, auth)
	if err != nil {
		return nil, err
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		// Best-effort purge; the filled credential is bad and the 401 the
		// caller reports is the failure.
		_ = c.Creds.CredentialReject(ctx, c.Endpoint, cred)
	case res.StatusCode == http.StatusForbidden:
		// The credential authenticated but lacks permission for this
		// operation; keep it — erasing a working login over an
		// authorization failure would force a needless re-prompt.
		_ = res.Body.Close()
		return nil, fmt.Errorf("%s: status 403: authenticated but not authorized (credential lacks permission)", c.Endpoint)
	case res.StatusCode/100 == 2:
		// Best-effort cache; a helper that cannot store must not fail the
		// transfer that just succeeded.
		_ = c.Creds.CredentialApprove(ctx, c.Endpoint, cred)
		c.auth = auth
	}
	return res, nil
}

func decodeBatch(endpoint, operation string, res *http.Response) ([]batchObject, error) {
	mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	isLFS := mt == MediaType || mt == "application/json"
	is2xx := res.StatusCode/100 == 2
	switch {
	case res.StatusCode == http.StatusOK && isLFS:
		var br batchResponse
		if err := json.NewDecoder(res.Body).Decode(&br); err != nil {
			return nil, fmt.Errorf("batch %s: decode: %w", operation, err)
		}
		if br.Transfer != "" && br.Transfer != "basic" {
			return nil, fmt.Errorf("batch %s: server chose transfer %q, want basic", operation, br.Transfer)
		}
		if br.HashAlgo != "" && br.HashAlgo != "sha256" {
			return nil, fmt.Errorf("batch %s: server hash_algo %q, want sha256", operation, br.HashAlgo)
		}
		return br.Objects, nil
	case res.StatusCode == http.StatusNotFound, res.StatusCode == http.StatusNotImplemented, is2xx && !isLFS:
		// The endpoint is not an LFS batch API: a 404/501, or a 2xx whose
		// body is not LFS JSON (a plain site answering OK). A transient
		// non-2xx (502/503) is NOT this — it falls through to a transport
		// error so callers never mistake an outage for a missing endpoint.
		return nil, fmt.Errorf("batch %s: %s (status %d): %w", operation, endpoint, res.StatusCode, ErrUnsupported)
	default:
		var ae apiError
		_ = json.NewDecoder(res.Body).Decode(&ae) // best-effort detail on an already-failed request
		return nil, fmt.Errorf("batch %s: %s: status %d: %s", operation, endpoint, res.StatusCode, ae.Message)
	}
}
