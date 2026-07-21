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
	first := testTenant(t)
	secondID, err := catalog.NewTenantID("cc-notes-second")
	if err != nil {
		t.Fatal(err)
	}
	second := Tenant{
		ID: secondID, Generation: 8, Authority: AuthorityForTenant(secondID),
		RouteName: "repo-second", RepoRoot: filepath.Join(t.TempDir(), "repo"),
	}
	policy := newHolderPolicy()

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
	prepareRoute := catalogservice.Route{Tenant: first.ID, Generation: first.Generation}
	authorization, err := policy.authorizeCatalog(catalogIdentity, catalogproto.OperationTenantPrepare, prepareRoute)
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
	if _, err := policy.authorizeCatalog(nativeCatalog, catalogproto.OperationDomainPrepare, catalogservice.Route{}); err == nil {
		t.Fatal("native session became a domain owner")
	}

	unboundSession, unboundClient := openAcceptedSession(t)
	unboundIdentity := catalogservice.Identity{Peer: unboundSession.Peer(), Build: unboundSession.Build(), Session: unboundSession}
	if _, err := policy.authorizeCatalog(unboundIdentity, catalogproto.OperationDomainPrepare, catalogservice.Route{}); err == nil {
		t.Fatal("unbound session accessed a protected catalog operation")
	}
	admin, err := policy.authorizeCatalog(
		unboundIdentity, catalogproto.OperationSourceAuthorityReadDesiredFleet, catalogservice.Route{},
	)
	if err != nil || admin.Role != catalogservice.RoleProductAdmin || admin.Principal != string(holderOwner) {
		t.Fatalf("product admin authorization = %+v err=%v", admin, err)
	}
	if _, err := policy.authorizeCatalog(unboundIdentity, catalogproto.OperationSourceAuthorityPublishDesiredFleet, catalogservice.Route{}); err != nil {
		t.Fatalf("reuse product admin session: %v", err)
	}
	if _, err := policy.authorizeCatalog(unboundIdentity, catalogproto.OperationTenantPrepare, prepareRoute); err == nil {
		t.Fatal("product admin session became a tenant owner")
	}

	if err := productClient.Close(); err != nil {
		t.Fatalf("close product session: %v", err)
	}
	waitBindingReleased(t, policy, productSession)
	_ = nativeClient.Close()
	_ = unboundClient.Close()
}

func TestHolderPolicyStartsEmptyAndBindsFirstExactTenantGeneration(t *testing.T) {
	policy := newHolderPolicy()
	tenantID, err := catalog.NewTenantID("cc-notes-dynamic")
	if err != nil {
		t.Fatal(err)
	}
	session, client := openAcceptedSession(t)
	identity := mountservice.Identity{Peer: session.Peer(), Build: session.Build(), Session: session}
	if _, err := policy.authorizeMount(
		t.Context(), identity, mountproto.OperationTenantProvision, tenantID, 7,
	); err != nil {
		t.Fatalf("authorize first desired tenant: %v", err)
	}
	if _, err := policy.authorizeMount(
		t.Context(), identity, mountproto.OperationTenantReplace, tenantID, 8,
	); err == nil {
		t.Fatal("session crossed its bound tenant generation")
	}
	_ = client.Close()
}

func TestHolderPolicyTenantStateCannotUnlockPreparation(t *testing.T) {
	policy := newHolderPolicy()
	tenant := testTenant(t)
	session, client := openAcceptedSession(t)
	identity := mountservice.Identity{Peer: session.Peer(), Build: session.Build(), Session: session}
	owner, err := policy.authorizeMount(
		t.Context(), identity, mountproto.OperationTenantState, tenant.ID, 0,
	)
	if err != nil || owner != holderOwner {
		t.Fatalf("authorize state owner=%q err=%v", owner, err)
	}
	catalogIdentity := catalogservice.Identity{Peer: session.Peer(), Build: session.Build(), Session: session}
	if _, err := policy.authorizeCatalog(
		catalogIdentity, catalogproto.OperationTenantPrepare,
		catalogservice.Route{Tenant: tenant.ID, Generation: tenant.Generation},
	); err == nil {
		t.Fatal("tenant state session unlocked tenant preparation")
	}
	_ = client.Close()
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
	go func() { done <- server.Serve(ctx, listener, func() error { return nil }, admit, admit) }()
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
