package fusefs

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/catalogservice"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/mountservice"
	"github.com/yasyf/fusekit/tenant"
	"github.com/yasyf/fusekit/transportproto"
)

const helperOwner tenant.OwnerID = "cc-notes"

// HelperRuntimeConfig defines the cc-notes product policy embedded by its fixed signed app.
type HelperRuntimeConfig struct {
	Plan                    holder.RuntimePlan
	Drivers                 holder.DriverFactories
	StopRole                string
	StopControlStore        proc.StopControlStore
	WorkerLimit             int
	NativeOptions           []string
	NativeReadinessTimeout  time.Duration
	NativeStdout            io.Writer
	NativeStderr            io.Writer
	SourceReadinessTimeout  time.Duration
	SourceStdout            io.Writer
	SourceStderr            io.Writer
	CatalogReadinessTimeout time.Duration
	CatalogOperationTimeout time.Duration
	CatalogStdout           io.Writer
	CatalogStderr           io.Writer
	ShutdownTimeout         time.Duration
	Signals                 <-chan os.Signal
}

// NewHelperRuntime composes cc-notes policy with FuseKit's production runtime.
func NewHelperRuntime(ctx context.Context, config HelperRuntimeConfig) (*holder.Runtime, error) {
	policy := newHelperPolicy()
	return holder.New(ctx, holder.Config{
		Plan: config.Plan, RuntimeBuild: config.Plan.BuildID(),
		Owner: catalog.SourceAuthorityFleetOwnerID(helperOwner), Drivers: config.Drivers,
		StopRole: config.StopRole, StopControlStore: config.StopControlStore,
		CatalogAuthorizer: catalogAuthorizer{policy}, Authorizer: mountAuthorizer{policy},
		WorkerLimit: config.WorkerLimit, NativeOptions: config.NativeOptions,
		NativeReadinessTimeout: config.NativeReadinessTimeout,
		NativeStdout:           config.NativeStdout, NativeStderr: config.NativeStderr,
		SourceReadinessTimeout: config.SourceReadinessTimeout,
		SourceStdout:           config.SourceStdout, SourceStderr: config.SourceStderr,
		CatalogReadinessTimeout: config.CatalogReadinessTimeout,
		CatalogOperationTimeout: config.CatalogOperationTimeout,
		CatalogStdout:           config.CatalogStdout, CatalogStderr: config.CatalogStderr,
		ShutdownTimeout: config.ShutdownTimeout, Signals: config.Signals,
	})
}

type helperSessionRole uint8

const (
	helperSessionProduct helperSessionRole = iota + 1
	helperSessionNative
	helperSessionAdmin
)

type helperSessionBinding struct {
	role       helperSessionRole
	tenant     catalog.TenantID
	generation catalog.Generation
}

type helperPolicy struct {
	uid int

	mu       sync.Mutex
	bindings map[*wire.AcceptedSession]helperSessionBinding
}

func newHelperPolicy() *helperPolicy {
	return &helperPolicy{uid: os.Getuid(), bindings: make(map[*wire.AcceptedSession]helperSessionBinding)}
}

func (p *helperPolicy) authorizeMount(
	_ context.Context,
	identity mountservice.Identity,
	operation mountproto.Operation,
	tenantID catalog.TenantID,
	generation catalog.Generation,
) (tenant.OwnerID, error) {
	if !tenantLifecycleOperation(operation) {
		return "", mountservice.ErrUnauthorized
	}
	if _, err := catalog.NewTenantID(string(tenantID)); err != nil {
		return "", mountservice.ErrUnauthorized
	}
	if operation == mountproto.OperationTenantState {
		if generation != 0 || identity.Peer.UID != p.uid || identity.Session == nil {
			return "", mountservice.ErrUnauthorized
		}
		return helperOwner, nil
	}
	if generation == 0 {
		return "", mountservice.ErrUnauthorized
	}
	if err := p.bind(identity.Peer, identity.Session, helperSessionBinding{
		role: helperSessionProduct, tenant: tenantID, generation: generation,
	}); err != nil {
		return "", err
	}
	return helperOwner, nil
}

func (p *helperPolicy) authorizeNative(
	_ context.Context,
	identity mountservice.Identity,
	operation mountproto.Operation,
) error {
	if !nativeOperation(operation) {
		return mountservice.ErrUnauthorized
	}
	return p.bind(identity.Peer, identity.Session, helperSessionBinding{role: helperSessionNative})
}

func (p *helperPolicy) authorizeObservation(
	_ context.Context,
	identity mountservice.ObservationIdentity,
	operation mountproto.Operation,
) error {
	if operation != mountproto.OperationRuntimeHealth ||
		identity.WireBuild != transportproto.WireBuild ||
		identity.Peer.PID <= 1 ||
		identity.Peer.UID != p.uid {
		return mountservice.ErrUnauthorized
	}
	return nil
}

func (p *helperPolicy) authorizeCatalog(
	identity catalogservice.Identity,
	operation catalogproto.Operation,
	route catalogservice.Route,
) (catalogservice.Authorization, error) {
	if productAdminOperation(operation) {
		if route != (catalogservice.Route{}) {
			return catalogservice.Authorization{}, errors.New("cc-notes helper: product admin request carries a tenant route")
		}
		if err := p.bind(identity.Peer, identity.Session, helperSessionBinding{role: helperSessionAdmin}); err != nil {
			return catalogservice.Authorization{}, err
		}
		return catalogservice.Authorization{
			Principal: string(helperOwner), Role: catalogservice.RoleProductAdmin, Route: route,
		}, nil
	}
	binding, err := p.bound(identity.Peer, identity.Session)
	if err != nil {
		return catalogservice.Authorization{}, err
	}
	principal := "cc-notes"
	switch binding.role {
	case helperSessionProduct:
		switch operation {
		case catalogproto.OperationTenantPrepare:
			if route.Tenant != binding.tenant || route.Generation != binding.generation || route.Forwarded || route.Domain != "" {
				return catalogservice.Authorization{}, errors.New("cc-notes helper: tenant preparation does not match the bound tenant")
			}
			return catalogservice.Authorization{Principal: principal, Role: catalogservice.RoleTenantOwner, Route: route}, nil
		default:
			return catalogservice.Authorization{}, errors.New("cc-notes helper: product session cannot access catalog presentation operations")
		}
	case helperSessionNative:
		if !catalogPresentationOperation(operation) || route.Forwarded || route.Domain != "" {
			return catalogservice.Authorization{}, errors.New("cc-notes helper: native session operation is not a mount presentation request")
		}
		return catalogservice.Authorization{
			Principal: principal, Role: catalogservice.RoleMount,
			Presentation: catalog.PresentationMount, Route: route,
		}, nil
	case helperSessionAdmin:
		return catalogservice.Authorization{}, errors.New("cc-notes helper: product admin session cannot access tenant operations")
	default:
		return catalogservice.Authorization{}, errors.New("cc-notes helper: invalid session role")
	}
}

func productAdminOperation(operation catalogproto.Operation) bool {
	return operation == catalogproto.OperationSourceAuthorityPublishDesiredFleet ||
		operation == catalogproto.OperationSourceAuthorityReadDesiredFleet
}

type mountAuthorizer struct{ policy *helperPolicy }

func (a mountAuthorizer) AuthorizeObservation(
	ctx context.Context,
	identity mountservice.ObservationIdentity,
	operation mountproto.Operation,
) error {
	return a.policy.authorizeObservation(ctx, identity, operation)
}

func (a mountAuthorizer) Authorize(
	ctx context.Context,
	identity mountservice.Identity,
	operation mountproto.Operation,
	tenantID catalog.TenantID,
	generation catalog.Generation,
) (tenant.OwnerID, error) {
	return a.policy.authorizeMount(ctx, identity, operation, tenantID, generation)
}

func (a mountAuthorizer) AuthorizeNative(
	ctx context.Context,
	identity mountservice.Identity,
	operation mountproto.Operation,
) error {
	return a.policy.authorizeNative(ctx, identity, operation)
}

type catalogAuthorizer struct{ policy *helperPolicy }

func (a catalogAuthorizer) Authorize(
	_ context.Context,
	identity catalogservice.Identity,
	operation catalogproto.Operation,
	route catalogservice.Route,
) (catalogservice.Authorization, error) {
	return a.policy.authorizeCatalog(identity, operation, route)
}

func (p *helperPolicy) bind(peer wire.Peer, session *wire.AcceptedSession, binding helperSessionBinding) error {
	if session == nil || peer.UID != p.uid {
		return errors.New("cc-notes helper: unauthenticated session")
	}
	p.mu.Lock()
	existing, exists := p.bindings[session]
	if exists {
		p.mu.Unlock()
		if existing != binding {
			return errors.New("cc-notes helper: persistent session is already bound to a different tenant or role")
		}
		return nil
	}
	p.bindings[session] = binding
	p.mu.Unlock()
	go p.releaseWhenDone(session, binding)
	return nil
}

func (p *helperPolicy) bound(peer wire.Peer, session *wire.AcceptedSession) (helperSessionBinding, error) {
	if session == nil || peer.UID != p.uid {
		return helperSessionBinding{}, errors.New("cc-notes helper: unauthenticated session")
	}
	p.mu.Lock()
	binding, ok := p.bindings[session]
	p.mu.Unlock()
	if !ok {
		return helperSessionBinding{}, errors.New("cc-notes helper: session has not provisioned or bound a tenant")
	}
	return binding, nil
}

func (p *helperPolicy) releaseWhenDone(session *wire.AcceptedSession, binding helperSessionBinding) {
	<-session.Done()
	p.mu.Lock()
	if current, ok := p.bindings[session]; ok && current == binding {
		delete(p.bindings, session)
	}
	p.mu.Unlock()
}

func tenantLifecycleOperation(operation mountproto.Operation) bool {
	switch operation {
	case mountproto.OperationTenantProvision, mountproto.OperationTenantReplace,
		mountproto.OperationTenantRemove, mountproto.OperationTenantState:
		return true
	default:
		return false
	}
}

func nativeOperation(operation mountproto.Operation) bool {
	switch operation {
	case mountproto.OperationNativeBind, mountproto.OperationNativeReady,
		mountproto.OperationNativeUnbind, mountproto.OperationNativeRoutePage, mountproto.OperationNativePin,
		mountproto.OperationNativeRelease:
		return true
	default:
		return false
	}
}

func catalogPresentationOperation(operation catalogproto.Operation) bool {
	switch operation {
	case catalogproto.OperationCatalogRoot, catalogproto.OperationCatalogHead,
		catalogproto.OperationCatalogSnapshot, catalogproto.OperationCatalogChangesSince,
		catalogproto.OperationCatalogLookup, catalogproto.OperationCatalogLookupName,
		catalogproto.OperationCatalogOpenAt, catalogproto.OperationCatalogMutate:
		return true
	default:
		return false
	}
}

var (
	_ mountservice.Authorizer   = mountAuthorizer{}
	_ catalogservice.Authorizer = catalogAuthorizer{}
)
