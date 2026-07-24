package fusefs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit/daemon"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/trust"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/daemonkit/worker"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/catalogservice"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/mountservice"
	"github.com/yasyf/fusekit/transportproto"
)

func TestHolderConfigCarriesExactProductRuntimeBudgets(t *testing.T) {
	config := newHolderConfig(HelperRuntimeConfig{
		NativeReadinessTimeout:  helpercontract.RuntimeNativeReadinessTimeout,
		CatalogReadinessTimeout: helpercontract.RuntimeCatalogReadinessTimeout,
		CatalogOperationTimeout: helpercontract.RuntimeCatalogOperationTimeout,
		ShutdownTimeout:         helpercontract.RuntimeShutdownTimeout,
	})
	if config.NativeReadinessTimeout != helpercontract.RuntimeNativeReadinessTimeout ||
		config.CatalogReadinessTimeout != helpercontract.RuntimeCatalogReadinessTimeout ||
		config.CatalogOperationTimeout != helpercontract.RuntimeCatalogOperationTimeout ||
		config.ShutdownTimeout != helpercontract.RuntimeShutdownTimeout {
		t.Fatalf("holder runtime budgets = (%s, %s, %s, %s)",
			config.NativeReadinessTimeout,
			config.CatalogReadinessTimeout, config.CatalogOperationTimeout, config.ShutdownTimeout)
	}
}

func TestHelperPolicyAuthorizesOnlyExactRuntimeHealthIdentity(t *testing.T) {
	policy := newHelperPolicy()
	identity := mountservice.ObservationIdentity{
		Peer: wire.Peer{PID: os.Getpid(), UID: os.Getuid()}, WireBuild: transportproto.WireBuild,
	}
	if err := policy.authorizeObservation(t.Context(), identity, mountproto.OperationRuntimeHealth); err != nil {
		t.Fatalf("authorize runtime health: %v", err)
	}

	tests := []struct {
		name      string
		identity  mountservice.ObservationIdentity
		operation mountproto.Operation
	}{
		{name: "wrong operation", identity: identity, operation: mountproto.OperationNativeBind},
		{name: "wrong uid", identity: mountservice.ObservationIdentity{
			Peer:      wire.Peer{PID: identity.Peer.PID, UID: identity.Peer.UID + 1},
			WireBuild: identity.WireBuild,
		}, operation: mountproto.OperationRuntimeHealth},
		{name: "invalid pid", identity: mountservice.ObservationIdentity{
			Peer:      wire.Peer{PID: 1, UID: identity.Peer.UID},
			WireBuild: identity.WireBuild,
		}, operation: mountproto.OperationRuntimeHealth},
		{name: "wrong build", identity: mountservice.ObservationIdentity{
			Peer: identity.Peer, WireBuild: "wrong-build",
		}, operation: mountproto.OperationRuntimeHealth},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := policy.authorizeObservation(t.Context(), test.identity, test.operation); !errors.Is(err, mountservice.ErrUnauthorized) {
				t.Fatalf("authorize runtime = %v, want %v", err, mountservice.ErrUnauthorized)
			}
		})
	}
}

func TestNativeOperationAuthorizationCoversExactProtocolSurface(t *testing.T) {
	allowed := []mountproto.Operation{
		mountproto.OperationNativeBind,
		mountproto.OperationNativeMounted,
		mountproto.OperationNativeReady,
		mountproto.OperationNativeUnbind,
		mountproto.OperationNativeRoutePage,
		mountproto.OperationNativePin,
		mountproto.OperationNativeRelease,
		mountproto.OperationNativeSnapshotOpen,
		mountproto.OperationNativeSnapshotRead,
		mountproto.OperationNativeSnapshotClose,
		mountproto.OperationNativeWriteOpen,
		mountproto.OperationNativeWriteRead,
		mountproto.OperationNativeWriteWrite,
		mountproto.OperationNativeWriteTruncate,
		mountproto.OperationNativeWriteSync,
		mountproto.OperationNativeWriteCommit,
		mountproto.OperationNativeWriteAbort,
	}
	for _, operation := range allowed {
		if !nativeOperation(operation) {
			t.Errorf("native operation %q was denied", operation)
		}
	}
	for _, operation := range []mountproto.Operation{
		"",
		mountproto.OperationRuntimeHealth,
		mountproto.OperationTenantProvision,
		mountproto.OperationTenantState,
	} {
		if nativeOperation(operation) {
			t.Errorf("non-native operation %q was allowed", operation)
		}
	}
}

func TestHelperPolicyFencesTenantAndRoleForSessionLifetime(t *testing.T) {
	first := testTenant(t)
	secondID, err := catalog.NewTenantID("cc-notes-second")
	if err != nil {
		t.Fatal(err)
	}
	second := Tenant{
		ID: secondID, Generation: 8, Authority: AuthorityForTenant(secondID),
		RouteName: "repo-second", RepoRoot: filepath.Join(t.TempDir(), "repo"),
	}
	policy := newHelperPolicy()

	productSession, productClient := openAcceptedSession(t)
	productIdentity := mountservice.Identity{Peer: productSession.Peer(), WireBuild: productSession.WireBuild(), Session: productSession}
	owner, err := policy.authorizeMount(t.Context(), productIdentity, mountproto.OperationTenantProvision, first.ID, first.Generation)
	if err != nil || owner != helperOwner {
		t.Fatalf("authorize product owner=%q err=%v", owner, err)
	}
	if _, err := policy.authorizeMount(t.Context(), productIdentity, mountproto.OperationTenantProvision, second.ID, second.Generation); err == nil {
		t.Fatal("cross-tenant session reuse succeeded")
	}
	catalogIdentity := catalogservice.Identity{Peer: productSession.Peer(), WireBuild: productSession.WireBuild(), Session: productSession}
	prepareRoute := catalogservice.Route{Tenant: first.ID, Generation: first.Generation}
	authorization, err := policy.authorizeCatalog(catalogIdentity, catalogproto.OperationTenantPrepare, prepareRoute)
	if err != nil || authorization.Role != catalogservice.RoleTenantOwner {
		t.Fatalf("prepare authorization = %+v err=%v", authorization, err)
	}
	if _, err := policy.authorizeCatalog(catalogIdentity, catalogproto.OperationCatalogHead, prepareRoute); err == nil {
		t.Fatal("product session became a mount presentation")
	}

	nativeSession, nativeClient := openAcceptedSession(t)
	nativeIdentity := mountservice.Identity{Peer: nativeSession.Peer(), WireBuild: nativeSession.WireBuild(), Session: nativeSession}
	if err := policy.authorizeNative(t.Context(), nativeIdentity, mountproto.OperationNativeBind); err != nil {
		t.Fatalf("authorize native: %v", err)
	}
	nativeCatalog := catalogservice.Identity{Peer: nativeSession.Peer(), WireBuild: nativeSession.WireBuild(), Session: nativeSession}
	authorization, err = policy.authorizeCatalog(nativeCatalog, catalogproto.OperationCatalogHead, prepareRoute)
	if err != nil || authorization.Role != catalogservice.RoleMount || authorization.Presentation != catalog.PresentationMount {
		t.Fatalf("native catalog authorization = %+v err=%v", authorization, err)
	}
	if _, err := policy.authorizeCatalog(nativeCatalog, catalogproto.OperationActivationAck, catalogservice.Route{}); err == nil {
		t.Fatal("native session became a domain owner")
	}

	unboundSession, unboundClient := openAcceptedSession(t)
	unboundIdentity := catalogservice.Identity{Peer: unboundSession.Peer(), WireBuild: unboundSession.WireBuild(), Session: unboundSession}
	if _, err := policy.authorizeCatalog(unboundIdentity, catalogproto.OperationActivationAck, catalogservice.Route{}); err == nil {
		t.Fatal("unbound session accessed a protected catalog operation")
	}
	admin, err := policy.authorizeCatalog(
		unboundIdentity, catalogproto.OperationSourceAuthorityReadDesiredFleet, catalogservice.Route{},
	)
	if err != nil || admin.Role != catalogservice.RoleProductAdmin || admin.Principal != string(helperOwner) {
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

func TestHelperPolicyStartsEmptyAndBindsFirstExactTenantGeneration(t *testing.T) {
	policy := newHelperPolicy()
	tenantID, err := catalog.NewTenantID("cc-notes-dynamic")
	if err != nil {
		t.Fatal(err)
	}
	session, client := openAcceptedSession(t)
	identity := mountservice.Identity{Peer: session.Peer(), WireBuild: session.WireBuild(), Session: session}
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

func TestHelperPolicyTenantStateCannotUnlockPreparation(t *testing.T) {
	policy := newHelperPolicy()
	tenant := testTenant(t)
	session, client := openAcceptedSession(t)
	identity := mountservice.Identity{Peer: session.Peer(), WireBuild: session.WireBuild(), Session: session}
	owner, err := policy.authorizeMount(
		t.Context(), identity, mountproto.OperationTenantState, tenant.ID, 0,
	)
	if err != nil || owner != helperOwner {
		t.Fatalf("authorize state owner=%q err=%v", owner, err)
	}
	catalogIdentity := catalogservice.Identity{Peer: session.Peer(), WireBuild: session.WireBuild(), Session: session}
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
	directory, err := os.MkdirTemp("", "ccn-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	const captureOp wire.Op = "capture"
	ladder, err := wire.NewLadder(
		map[wire.Op]time.Duration{captureOp: time.Second},
		map[wire.Op]time.Duration{captureOp: 2 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	server := &wire.Server{WireBuild: "cc-notes-policy-test", Ladder: ladder}
	captured := make(chan *wire.AcceptedSession, 1)
	server.Register(wire.HandlerSpec{
		Op: captureOp, Concurrent: true,
		Handler: func(_ context.Context, request wire.Request) (any, error) {
			captured <- request.Session
			return json.RawMessage(`{}`), nil
		},
	})
	generation, err := proc.ProcessGeneration()
	if err != nil {
		t.Fatal(err)
	}
	reaper := func(name string) *proc.Reaper {
		return &proc.Reaper{
			Store: &proc.FileStore{Path: filepath.Join(directory, name+".db")}, Generation: generation,
			Grace: 10 * time.Millisecond, Settlement: time.Second,
		}
	}
	workers, err := worker.NewPool(worker.Config{
		Capacity: 2, QueueCapacity: 2, MaxTotalRun: 5 * time.Second,
		MaxStdinBytes: 4096, MaxStdoutBytes: 4096, MaxStderrBytes: 4096,
	}, reaper("workers"))
	if err != nil {
		t.Fatal(err)
	}
	children, err := proc.NewManager(2, reaper("children"))
	if err != nil {
		t.Fatal(err)
	}
	trustPolicy, err := trust.NewTrustPolicy(trust.TrustPolicyConfig{
		ExpectedUID: os.Geteuid(), AllowUnprotected: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "wire.sock")
	runtime, err := wire.NewRuntime(wire.RuntimeConfig{
		Socket: socket, RuntimeBuild: server.WireBuild, RuntimeProtocol: 1,
		Wire: server, TrustPolicy: trustPolicy,
		StopControlStore: &proc.FileStore{Path: filepath.Join(directory, "stop.db")},
		Workers:          workers, Children: children, ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	slot := daemon.NewPublicationSlot[struct{}](runtime)
	activation, err := runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	publication, err := slot.Stage(activation, struct{}{})
	if err != nil {
		_ = activation.Fail(err)
		t.Fatal(err)
	}
	if err := activation.CommitReady(publication); err != nil {
		_ = activation.Fail(err)
		t.Fatal(err)
	}
	client, err := wire.NewClient(t.Context(), wire.ClientConfig{
		Dial: wire.UnixDialer(socket), WireBuild: server.WireBuild,
		Role: trust.UnprotectedRole, Ladder: ladder,
	})
	if err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = runtime.Close(closeCtx)
		t.Fatal(err)
	}
	if _, err := client.Call(t.Context(), captureOp, "", nil); err != nil {
		_ = client.Close()
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = runtime.Close(closeCtx)
		t.Fatal(err)
	}
	session := <-captured
	t.Cleanup(func() {
		_ = client.Close()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runtime.Close(closeCtx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("wire runtime: %v", err)
		}
	})
	return session, client
}

func waitBindingReleased(t *testing.T, policy *helperPolicy, session *wire.AcceptedSession) {
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
