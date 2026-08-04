//go:build darwin

package fusefs

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/catalogservice"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/mountservice"
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
		config.ShutdownTimeout != helpercontract.RuntimeShutdownTimeout ||
		len(config.BusinessHandlers) != 1 ||
		config.BusinessHandlers[0].Op != helpercontract.ProvisionRepositoryOperation ||
		config.BusinessHandlers[0].Handler == nil || config.BusinessHandlers[0].Concurrent {
		t.Fatalf("holder runtime budgets = (%s, %s, %s, %s)",
			config.NativeReadinessTimeout,
			config.CatalogReadinessTimeout, config.CatalogOperationTimeout, config.ShutdownTimeout)
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
		mountproto.OperationTenantProvision,
		mountproto.OperationTenantReplace,
		mountproto.OperationTenantRemove,
		mountproto.OperationTenantState,
	} {
		if nativeOperation(operation) {
			t.Errorf("non-native operation %q was allowed", operation)
		}
	}
}

func TestCatalogPresentationOperationCoversExactMountSurface(t *testing.T) {
	allowed := []catalogproto.Operation{
		catalogproto.OperationCatalogRoot,
		catalogproto.OperationCatalogHead,
		catalogproto.OperationCatalogSnapshot,
		catalogproto.OperationCatalogChangesSince,
		catalogproto.OperationCatalogLookup,
		catalogproto.OperationCatalogLookupName,
		catalogproto.OperationCatalogOpenAt,
		catalogproto.OperationCatalogMutateBegin,
	}
	for _, operation := range allowed {
		if !catalogPresentationOperation(operation) {
			t.Errorf("mount presentation operation %q was denied", operation)
		}
	}
	for _, operation := range []catalogproto.Operation{
		"",
		catalogproto.OperationCatalogLookupPrivate,
		catalogproto.OperationCatalogOpenPrivate,
		catalogproto.OperationActivationAck,
		catalogproto.OperationActivationPoll,
		catalogproto.OperationBrokerPoll,
		catalogproto.OperationBrokerResult,
		catalogproto.OperationTenantPrepare,
		catalogproto.OperationSourceAuthorityReadDesiredFleet,
	} {
		if catalogPresentationOperation(operation) {
			t.Errorf("non-presentation operation %q was allowed", operation)
		}
	}
}

func TestHelperPolicyExposesOnlyNativePresentationSessions(t *testing.T) {
	policy := newHelperPolicy()
	tenantID, err := catalog.NewTenantID("cc-notes-native")
	if err != nil {
		t.Fatal(err)
	}
	route := catalogservice.Route{Tenant: tenantID, Generation: 1}
	sessions := startSessionDaemon(t)

	product, closeProduct := sessions.accept(t)
	productIdentity := mountservice.Identity{Caller: product.Caller, Session: product.Session}
	if _, err := policy.authorizeMount(
		t.Context(), productIdentity, mountproto.OperationTenantProvision, tenantID, 1,
	); !errors.Is(err, mountservice.ErrUnauthorized) {
		t.Fatalf("product tenant operation = %v, want unauthorized", err)
	}
	productCatalog := catalogservice.Identity{Caller: product.Caller, Session: product.Session}
	if _, err := policy.authorizeCatalog(
		productCatalog, catalogproto.OperationSourceAuthorityReadDesiredFleet, catalogservice.Route{},
	); err == nil {
		t.Fatal("product source-fleet operation was exposed")
	}
	closeProduct()

	native, closeNative := sessions.accept(t)
	nativeIdentity := mountservice.Identity{Caller: native.Caller, Session: native.Session}
	if err := policy.authorizeNative(t.Context(), nativeIdentity, mountproto.OperationNativeBind); err != nil {
		t.Fatalf("authorize native: %v", err)
	}
	nativeCatalog := catalogservice.Identity{Caller: native.Caller, Session: native.Session}
	authorization, err := policy.authorizeCatalog(nativeCatalog, catalogproto.OperationCatalogHead, route)
	if err != nil || authorization.Role != catalogservice.RoleMount || authorization.Presentation != catalog.PresentationMount {
		t.Fatalf("native catalog authorization = %+v err=%v", authorization, err)
	}
	if _, err := policy.authorizeCatalog(nativeCatalog, catalogproto.OperationActivationAck, catalogservice.Route{}); err == nil {
		t.Fatal("native session became a domain owner")
	}

	unbound, closeUnbound := sessions.accept(t)
	unboundIdentity := catalogservice.Identity{Caller: unbound.Caller, Session: unbound.Session}
	if _, err := policy.authorizeCatalog(unboundIdentity, catalogproto.OperationActivationAck, catalogservice.Route{}); err == nil {
		t.Fatal("unbound session accessed a protected catalog operation")
	}

	closeNative()
	waitBindingReleased(t, policy, native.Session)
	closeUnbound()
}

const sessionCaptureOp = "product.cc-notes.session.capture.v1"

type sessionDaemon struct {
	daemon   daemonkit.Daemon
	accepted chan daemonkit.Request
}

func startSessionDaemon(t *testing.T) *sessionDaemon {
	t.Helper()
	// daemonkit derives its socket from the home root, and darwin's sun_path
	// fits 103 bytes; homeguard's redirect root already spends 87 of them.
	home, err := os.MkdirTemp("/tmp", "ccn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("DAEMONKIT_HOME", home)

	sessions := &sessionDaemon{
		daemon: daemonkit.Daemon{
			Label:     "ccn-fusefs-policy",
			Schemas:   []daemonkit.Schema{"cc-notes-policy-test"},
			Trust:     daemonkit.Trust{Serving: daemonkit.ServingSameUser()},
			Shutdown:  daemonkit.Grace(10 * time.Second),
			Handshake: daemonkit.Grace(10 * time.Second),
		},
		accepted: make(chan daemonkit.Request, 1),
	}
	serveCtx, stopServing := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		_, err := daemonkit.Serve(serveCtx, sessions.daemon, func(daemonkit.Ctx) (daemonkit.Product, error) {
			return sessions, nil
		})
		served <- err
	}()
	t.Cleanup(func() {
		stopServing()
		select {
		case err := <-served:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("daemon did not return after drain")
		}
	})
	return sessions
}

func (d *sessionDaemon) Handle(_ context.Context, request daemonkit.Request) (daemonkit.Reply, error) {
	d.accepted <- request
	return daemonkit.Reply{Body: []byte(`{}`)}, nil
}

func (*sessionDaemon) Drain(daemonkit.Budget) error { return nil }

func (*sessionDaemon) Close(daemonkit.Budget) error { return nil }

func (d *sessionDaemon) accept(t *testing.T) (daemonkit.Request, func()) {
	t.Helper()
	client, err := daemonkit.Open(d.daemon)
	if err != nil {
		t.Fatal(err)
	}
	business := client.Business()
	closeLane := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := business.Close(ctx); err != nil {
			t.Errorf("close business lane: %v", err)
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		callCtx, cancel := context.WithTimeout(t.Context(), time.Second)
		_, err := business.Call(callCtx, sessionCaptureOp, []byte(`{}`))
		cancel()
		if err == nil {
			return <-d.accepted, closeLane
		}
		if time.Now().After(deadline) {
			closeLane()
			t.Fatalf("daemon did not admit a session: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitBindingReleased(t *testing.T, policy *helperPolicy, session daemonkit.Session) {
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
