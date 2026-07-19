package fusefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/catalogservice"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/mountservice"
	"github.com/yasyf/fusekit/tenant"
	"github.com/yasyf/fusekit/transportproto"
)

const holderOwner tenant.OwnerID = "cc-notes"

// HolderRuntimeConfig defines the cc-notes product policy embedded by its fixed signed app.
type HolderRuntimeConfig struct {
	Plan                   holder.Plan
	Tenants                []Tenant
	WorkerLimit            int
	NativeOptions          []string
	NativeReadinessTimeout time.Duration
	NativeStdout           io.Writer
	NativeStderr           io.Writer
	ShutdownTimeout        time.Duration
	Signals                <-chan os.Signal
}

// NewHolderRuntime composes cc-notes policy with FuseKit's production holder runtime.
func NewHolderRuntime(ctx context.Context, config HolderRuntimeConfig) (*holder.Runtime, error) {
	policy, err := newHolderPolicy(config.Plan, config.Tenants)
	if err != nil {
		return nil, err
	}
	planner := sourceMutationPlanner{executable: config.Plan.Executable(), tenants: policy.tenants}
	return holder.New(ctx, holder.Config{
		Plan: config.Plan, Build: transportproto.Build,
		SourceMutation: planner, CatalogAuthorizer: catalogAuthorizer{policy}, Authorizer: mountAuthorizer{policy},
		WorkerLimit: config.WorkerLimit, NativeOptions: config.NativeOptions,
		NativeReadinessTimeout: config.NativeReadinessTimeout,
		NativeStdout:           config.NativeStdout, NativeStderr: config.NativeStderr,
		ShutdownTimeout: config.ShutdownTimeout, Signals: config.Signals,
	})
}

type holderSessionRole uint8

const (
	holderSessionProduct holderSessionRole = iota + 1
	holderSessionNative
)

type holderSessionBinding struct {
	role   holderSessionRole
	tenant catalog.TenantID
}

type holderPolicy struct {
	uid     int
	tenants map[catalog.TenantID]Tenant

	mu       sync.Mutex
	bindings map[*wire.AcceptedSession]holderSessionBinding
}

func newHolderPolicy(plan holder.Plan, tenants []Tenant) (*holderPolicy, error) {
	if len(tenants) == 0 {
		return nil, errors.New("cc-notes holder: at least one tenant is required")
	}
	configured := make(map[catalog.TenantID]Tenant, len(tenants))
	authorities := make(map[catalogproto.SourceAuthorityID]catalog.TenantID, len(tenants))
	for _, candidate := range tenants {
		if err := candidate.Validate(plan); err != nil {
			return nil, err
		}
		if _, exists := configured[candidate.ID]; exists {
			return nil, fmt.Errorf("cc-notes holder: duplicate tenant %q", candidate.ID)
		}
		if owner, exists := authorities[candidate.Authority]; exists {
			return nil, fmt.Errorf("cc-notes holder: source authority %q is shared by tenants %q and %q", candidate.Authority, owner, candidate.ID)
		}
		configured[candidate.ID] = candidate
		authorities[candidate.Authority] = candidate.ID
	}
	return &holderPolicy{uid: os.Getuid(), tenants: configured, bindings: make(map[*wire.AcceptedSession]holderSessionBinding)}, nil
}

func (p *holderPolicy) authorizeMount(
	_ context.Context,
	identity mountservice.Identity,
	operation mountproto.Operation,
	tenantID catalog.TenantID,
	generation catalog.Generation,
) (tenant.OwnerID, error) {
	if !tenantLifecycleOperation(operation) {
		return "", mountservice.ErrUnauthorized
	}
	configured, ok := p.tenants[tenantID]
	if !ok || configured.Generation != generation {
		return "", mountservice.ErrUnauthorized
	}
	if err := p.bind(identity.Peer, identity.Session, holderSessionBinding{role: holderSessionProduct, tenant: tenantID}); err != nil {
		return "", err
	}
	return holderOwner, nil
}

func (p *holderPolicy) authorizeNative(
	_ context.Context,
	identity mountservice.Identity,
	operation mountproto.Operation,
) error {
	if !nativeOperation(operation) {
		return mountservice.ErrUnauthorized
	}
	return p.bind(identity.Peer, identity.Session, holderSessionBinding{role: holderSessionNative})
}

func (p *holderPolicy) authorizeCatalog(
	identity catalogservice.Identity,
	operation catalogproto.Operation,
	route catalogservice.Route,
) (catalogservice.Authorization, error) {
	binding, err := p.bound(identity.Peer, identity.Session)
	if err != nil {
		return catalogservice.Authorization{}, err
	}
	principal := "cc-notes"
	switch binding.role {
	case holderSessionProduct:
		configured := p.tenants[binding.tenant]
		switch operation {
		case catalogproto.OperationSourceReconcile:
			if route != (catalogservice.Route{}) {
				return catalogservice.Authorization{}, errors.New("cc-notes holder: source publication carries a tenant route")
			}
			return catalogservice.Authorization{
				Principal: principal, Role: catalogservice.RoleSourcePublisher,
				Route: route, SourceAuthority: causal.SourceAuthorityID(configured.Authority),
			}, nil
		case catalogproto.OperationTenantPrepare:
			if route.Tenant != binding.tenant || route.Generation != configured.Generation || route.Forwarded || route.Domain != "" {
				return catalogservice.Authorization{}, errors.New("cc-notes holder: tenant preparation does not match the bound tenant")
			}
			return catalogservice.Authorization{Principal: principal, Role: catalogservice.RoleTenantOwner, Route: route}, nil
		default:
			return catalogservice.Authorization{}, errors.New("cc-notes holder: product session cannot access catalog presentation operations")
		}
	case holderSessionNative:
		if !catalogPresentationOperation(operation) || route.Forwarded || route.Domain != "" {
			return catalogservice.Authorization{}, errors.New("cc-notes holder: native session operation is not a mount presentation request")
		}
		configured, ok := p.tenants[route.Tenant]
		if !ok || configured.Generation != route.Generation {
			return catalogservice.Authorization{}, errors.New("cc-notes holder: native catalog route is not configured")
		}
		return catalogservice.Authorization{
			Principal: principal, Role: catalogservice.RoleMount,
			Presentation: catalog.PresentationMount, Route: route,
		}, nil
	default:
		return catalogservice.Authorization{}, errors.New("cc-notes holder: invalid session role")
	}
}

type mountAuthorizer struct{ policy *holderPolicy }

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

type catalogAuthorizer struct{ policy *holderPolicy }

func (a catalogAuthorizer) Authorize(
	_ context.Context,
	identity catalogservice.Identity,
	operation catalogproto.Operation,
	route catalogservice.Route,
) (catalogservice.Authorization, error) {
	return a.policy.authorizeCatalog(identity, operation, route)
}

func (p *holderPolicy) bind(peer wire.Peer, session *wire.AcceptedSession, binding holderSessionBinding) error {
	if session == nil || peer.UID != p.uid {
		return errors.New("cc-notes holder: unauthenticated session")
	}
	p.mu.Lock()
	existing, exists := p.bindings[session]
	if exists {
		p.mu.Unlock()
		if existing != binding {
			return errors.New("cc-notes holder: persistent session is already bound to a different tenant or role")
		}
		return nil
	}
	p.bindings[session] = binding
	p.mu.Unlock()
	go p.releaseWhenDone(session, binding)
	return nil
}

func (p *holderPolicy) bound(peer wire.Peer, session *wire.AcceptedSession) (holderSessionBinding, error) {
	if session == nil || peer.UID != p.uid {
		return holderSessionBinding{}, errors.New("cc-notes holder: unauthenticated session")
	}
	p.mu.Lock()
	binding, ok := p.bindings[session]
	p.mu.Unlock()
	if !ok {
		return holderSessionBinding{}, errors.New("cc-notes holder: session has not provisioned or bound a tenant")
	}
	return binding, nil
}

func (p *holderPolicy) releaseWhenDone(session *wire.AcceptedSession, binding holderSessionBinding) {
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
		mountproto.OperationNativeRoutes, mountproto.OperationNativePin,
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
