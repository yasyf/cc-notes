package fusefs

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/catalogservice"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/mountservice"
)

func TestHolderPolicyFencesTenantAndRoleForSessionLifetime(t *testing.T) {
	plan := testPlan(t)
	first := testTenant(t)
	secondID, err := catalog.NewTenantID("cc-notes-second")
	if err != nil {
		t.Fatal(err)
	}
	second := Tenant{
		ID: secondID, Generation: 8, Authority: AuthorityForTenant(secondID),
		Domain: "mount:cc-notes-second", RouteName: "repo-second", RepoRoot: filepath.Join(t.TempDir(), "repo"),
	}
	policy, err := newHolderPolicy(plan, []Tenant{first, second})
	if err != nil {
		t.Fatalf("newHolderPolicy: %v", err)
	}

	productSession, productClient := openAcceptedSession(t)
	productIdentity := mountservice.Identity{Peer: productSession.Peer(), Build: productSession.Build(), Session: productSession}
	owner, err := policy.authorizeMount(t.Context(), productIdentity, mountproto.OperationTenantProvision, first.ID, first.Generation)
	if err != nil || owner != holderOwner {
		t.Fatalf("authorize product owner=%q err=%v", owner, err)
	}
	if _, err := policy.authorizeMount(t.Context(), productIdentity, mountproto.OperationTenantProvision, second.ID, second.Generation); err == nil {
		t.Fatal("cross-tenant session reuse succeeded")
	}
	catalogIdentity := catalogservice.Identity{Peer: productSession.Peer(), Build: productSession.Build(), Session: productSession}
	authorization, err := policy.authorizeCatalog(catalogIdentity, catalogproto.OperationSourceReconcile, catalogservice.Route{})
	if err != nil || authorization.Role != catalogservice.RoleSourcePublisher || string(authorization.SourceAuthority) != string(first.Authority) {
		t.Fatalf("source authorization = %+v err=%v", authorization, err)
	}
	prepareRoute := catalogservice.Route{Tenant: first.ID, Generation: first.Generation}
	authorization, err = policy.authorizeCatalog(catalogIdentity, catalogproto.OperationTenantPrepare, prepareRoute)
	if err != nil || authorization.Role != catalogservice.RoleTenantOwner {
		t.Fatalf("prepare authorization = %+v err=%v", authorization, err)
	}
	if _, err := policy.authorizeCatalog(catalogIdentity, catalogproto.OperationCatalogHead, prepareRoute); err == nil {
		t.Fatal("product session became a mount presentation")
	}

	nativeSession, nativeClient := openAcceptedSession(t)
	nativeIdentity := mountservice.Identity{Peer: nativeSession.Peer(), Build: nativeSession.Build(), Session: nativeSession}
	if err := policy.authorizeNative(t.Context(), nativeIdentity, mountproto.OperationNativeBind); err != nil {
		t.Fatalf("authorize native: %v", err)
	}
	nativeCatalog := catalogservice.Identity{Peer: nativeSession.Peer(), Build: nativeSession.Build(), Session: nativeSession}
	authorization, err = policy.authorizeCatalog(nativeCatalog, catalogproto.OperationCatalogHead, prepareRoute)
	if err != nil || authorization.Role != catalogservice.RoleMount || authorization.Presentation != catalog.PresentationMount {
		t.Fatalf("native catalog authorization = %+v err=%v", authorization, err)
	}
	if _, err := policy.authorizeCatalog(nativeCatalog, catalogproto.OperationSourceReconcile, catalogservice.Route{}); err == nil {
		t.Fatal("native session became a source publisher")
	}

	unboundSession, unboundClient := openAcceptedSession(t)
	unboundIdentity := catalogservice.Identity{Peer: unboundSession.Peer(), Build: unboundSession.Build(), Session: unboundSession}
	if _, err := policy.authorizeCatalog(unboundIdentity, catalogproto.OperationSourceReconcile, catalogservice.Route{}); err == nil {
		t.Fatal("unbound session published a source snapshot")
	}

	if err := productClient.Close(); err != nil {
		t.Fatalf("close product session: %v", err)
	}
	waitBindingReleased(t, policy, productSession)
	_ = nativeClient.Close()
	_ = unboundClient.Close()
}

func TestHolderPolicyRejectsDuplicateConfiguredIdentity(t *testing.T) {
	plan := testPlan(t)
	configured := testTenant(t)
	if _, err := newHolderPolicy(plan, []Tenant{configured, configured}); err == nil {
		t.Fatal("duplicate tenant was accepted")
	}
}

func openAcceptedSession(t *testing.T) (*wire.AcceptedSession, *wire.Client) {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "ccn-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	listener, err := net.Listen("unix", filepath.Join(directory, "wire.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &wire.Server{Build: "cc-notes-policy-test"}
	captured := make(chan *wire.AcceptedSession, 1)
	server.RegisterConcurrent("capture", func(_ context.Context, request wire.Request) (any, error) {
		captured <- request.Session
		return json.RawMessage(`{}`), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	admit := func() (func(), error) { return func() {}, nil }
	go func() { done <- server.Serve(ctx, listener, admit, admit) }()
	client, err := wire.NewClient(t.Context(), wire.ClientConfig{
		Dial: wire.UnixDialer(listener.Addr().String()), Build: server.Build,
	})
	if err != nil {
		cancel()
		_ = listener.Close()
		t.Fatal(err)
	}
	if _, err := client.Call(t.Context(), "capture", "", nil); err != nil {
		_ = client.Close()
		cancel()
		_ = listener.Close()
		t.Fatal(err)
	}
	session := <-captured
	t.Cleanup(func() {
		_ = client.Close()
		cancel()
		_ = listener.Close()
		select {
		case serveErr := <-done:
			if serveErr != nil && !errors.Is(serveErr, context.Canceled) && !errors.Is(serveErr, net.ErrClosed) {
				t.Errorf("wire server: %v", serveErr)
			}
		case <-time.After(5 * time.Second):
			t.Error("wire server did not stop")
		}
	})
	return session, client
}

func waitBindingReleased(t *testing.T, policy *holderPolicy, session *wire.AcceptedSession) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		policy.mu.Lock()
		_, exists := policy.bindings[session]
		policy.mu.Unlock()
		if !exists {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("session binding was not released")
}
