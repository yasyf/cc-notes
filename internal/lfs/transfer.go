package lfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Upload pushes every object the server lacks: batch("upload"), then a PUT
// per returned upload action, then a POST of the verify action when present.
// Objects the server answers without actions are already there and are
// skipped, which makes re-upload a natural no-op. Per-object failures — a
// batch error, or a PUT or verify the server answers non-2xx — aggregate as
// *ObjectError values via errors.Join while the remaining objects still
// upload; a transport failure aborts immediately.
func (c *Client) Upload(ctx context.Context, store Store, objects []Object) (uploaded int, err error) {
	res, err := c.batch(ctx, "upload", objects)
	if err != nil {
		return 0, err
	}
	var objErrs []error
	for _, obj := range res {
		if obj.Error != nil {
			objErrs = append(objErrs, fmt.Errorf("upload: %w", &ObjectError{OID: obj.OID, Code: obj.Error.Code, Message: obj.Error.Message}))
			continue
		}
		act, ok := obj.Actions["upload"]
		if !ok {
			continue // no action: the server already has it
		}
		err := c.putObject(ctx, store, obj, act)
		if verify, ok := obj.Actions["verify"]; ok && err == nil {
			err = c.verifyObject(ctx, obj, verify)
		}
		if oe := (*ObjectError)(nil); errors.As(err, &oe) {
			objErrs = append(objErrs, err)
			continue
		}
		if err != nil {
			return uploaded, err
		}
		uploaded++
	}
	return uploaded, errors.Join(objErrs...)
}

// Download fetches every object into the store: batch("download"), then a
// GET per action streamed through hash verification, so a corrupt body
// never lands. Per-object server errors — a 404 means the ref referenced
// content nobody uploaded — aggregate as *ObjectError values via
// errors.Join; a transfer failure aborts immediately.
func (c *Client) Download(ctx context.Context, store Store, objects []Object) (downloaded int, err error) {
	res, err := c.batch(ctx, "download", objects)
	if err != nil {
		return 0, err
	}
	var objErrs []error
	for _, obj := range res {
		if obj.Error != nil {
			objErrs = append(objErrs, fmt.Errorf("download: %w", &ObjectError{OID: obj.OID, Code: obj.Error.Code, Message: obj.Error.Message}))
			continue
		}
		act, ok := obj.Actions["download"]
		if !ok {
			return downloaded, fmt.Errorf("download %s: no download action", obj.OID)
		}
		if err := c.getObject(ctx, store, obj, act); err != nil {
			return downloaded, err
		}
		downloaded++
	}
	return downloaded, errors.Join(objErrs...)
}

// putObject PUTs the object's content at the action href with an explicit
// Content-Length and application/octet-stream — S3-style pre-signed hrefs
// reject chunked or re-typed bodies — plus the action's own headers, nothing
// else.
func (c *Client) putObject(ctx context.Context, store Store, obj batchObject, act action) error {
	f, err := store.Open(obj.OID)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, act.Href, f)
	if err != nil {
		return err
	}
	req.ContentLength = obj.Size
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range act.Header {
		req.Header.Set(k, v)
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("put %s: %w", obj.OID, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("put: %w", statusError(obj.OID, res))
	}
	return nil
}

// VerifyBackoff spaces the verify retries. A server may answer the verify
// right after a successful PUT with a 4xx or 5xx until its storage sees the
// object — GitHub answers 422 — so each delay buys one more attempt before
// the object fails.
var VerifyBackoff = []time.Duration{time.Second, 3 * time.Second}

func (c *Client) verifyObject(ctx context.Context, obj batchObject, act action) error {
	body, err := json.Marshal(Object{OID: obj.OID, Size: obj.Size})
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		err := c.verifyOnce(ctx, obj, act, body)
		if oe := (*ObjectError)(nil); !errors.As(err, &oe) || attempt == len(VerifyBackoff) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(VerifyBackoff[attempt]):
		}
	}
}

func (c *Client) verifyOnce(ctx context.Context, obj batchObject, act action, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, act.Href, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", MediaType)
	req.Header.Set("Content-Type", MediaType)
	for k, v := range act.Header {
		req.Header.Set(k, v)
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("verify %s: %w", obj.OID, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("verify: %w", statusError(obj.OID, res))
	}
	return nil
}

// statusError reads a non-2xx content response into an *ObjectError carrying
// the server's message — the LFS JSON message when the body is one, else the
// body's first bytes — so the operator sees why the server refused.
func statusError(oid string, res *http.Response) *ObjectError {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	msg := strings.TrimSpace(string(raw))
	var api apiError
	if json.Unmarshal(raw, &api) == nil && api.Message != "" {
		msg = api.Message
	}
	return &ObjectError{OID: oid, Code: res.StatusCode, Message: msg}
}

func (c *Client) getObject(ctx context.Context, store Store, obj batchObject, act action) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, act.Href, nil)
	if err != nil {
		return err
	}
	for k, v := range act.Header {
		req.Header.Set(k, v)
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", obj.OID, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("get %s: status %d", obj.OID, res.StatusCode)
	}
	if err := store.PutVerified(res.Body, obj.OID, obj.Size); err != nil {
		return fmt.Errorf("get %s: %w", obj.OID, err)
	}
	return nil
}
