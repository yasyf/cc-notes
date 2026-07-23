package fusefs

import (
	"encoding/hex"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/mountproto"
)

func TestProvisionRuntimeRequiresExactHealthyActivation(t *testing.T) {
	const build = "v0.41.0"
	presentationRoot := filepath.Join(t.TempDir(), "mount")
	nativeSource, err := mountproto.NativeMountSource(presentationRoot)
	if err != nil {
		t.Fatal(err)
	}
	exact := mountproto.RuntimeHealthResponse{
		Protocol: mountproto.Version, Code: mountproto.ErrorCodeOk,
		RuntimeBuild: build, RuntimeProtocol: mountproto.RuntimeProtocolVersion, RuntimePID: 42,
		ProcessGeneration: "process-1", ActivationGeneration: "activation-1",
		State: mountproto.RuntimeStateHealthy, Ready: true,
		ReadinessPhase: mountproto.ReadinessPhaseReady, ReadinessStep: mountproto.ReadinessStepPublished,
		NativePhase: mountproto.NativePhaseLive,
		NativeMount: &mountproto.NativeMountProof{
			PresentationRoot: presentationRoot, Filesystem: mountproto.NativeMountFilesystem,
			Source: nativeSource, RootReadEpoch: 1,
		},
		BrokerPhase: mountproto.BrokerPhaseDisabled,
	}
	if err := validateProvisionRuntime(build, presentationRoot, exact); err != nil {
		t.Fatalf("validate exact runtime: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*mountproto.RuntimeHealthResponse)
	}{
		{name: "response protocol", mutate: func(health *mountproto.RuntimeHealthResponse) { health.Protocol++ }},
		{name: "response code", mutate: func(health *mountproto.RuntimeHealthResponse) { health.Code = mountproto.ErrorCodeUnavailable }},
		{name: "response message", mutate: func(health *mountproto.RuntimeHealthResponse) { health.Message = "unexpected" }},
		{name: "build", mutate: func(health *mountproto.RuntimeHealthResponse) { health.RuntimeBuild = "other" }},
		{name: "protocol", mutate: func(health *mountproto.RuntimeHealthResponse) { health.RuntimeProtocol++ }},
		{name: "pid", mutate: func(health *mountproto.RuntimeHealthResponse) { health.RuntimePID = 0 }},
		{name: "process generation", mutate: func(health *mountproto.RuntimeHealthResponse) { health.ProcessGeneration = "" }},
		{name: "activation generation", mutate: func(health *mountproto.RuntimeHealthResponse) { health.ActivationGeneration = "" }},
		{name: "readiness", mutate: func(health *mountproto.RuntimeHealthResponse) { health.Ready = false }},
		{name: "draining", mutate: func(health *mountproto.RuntimeHealthResponse) { health.Draining = true }},
		{name: "busy", mutate: func(health *mountproto.RuntimeHealthResponse) { health.Busy = true }},
		{name: "state", mutate: func(health *mountproto.RuntimeHealthResponse) { health.State = mountproto.RuntimeStateDegraded }},
		{name: "readiness phase", mutate: func(health *mountproto.RuntimeHealthResponse) {
			health.ReadinessPhase = mountproto.ReadinessPhaseStarting
		}},
		{name: "readiness step", mutate: func(health *mountproto.RuntimeHealthResponse) { health.ReadinessStep = mountproto.ReadinessStepNative }},
		{name: "native phase", mutate: func(health *mountproto.RuntimeHealthResponse) { health.NativePhase = mountproto.NativePhaseStarting }},
		{name: "native proof", mutate: func(health *mountproto.RuntimeHealthResponse) { health.NativeMount = nil }},
		{name: "native root", mutate: func(health *mountproto.RuntimeHealthResponse) {
			health.NativeMount.PresentationRoot = filepath.Join(t.TempDir(), "other")
		}},
		{name: "native filesystem", mutate: func(health *mountproto.RuntimeHealthResponse) { health.NativeMount.Filesystem = "other" }},
		{name: "native source", mutate: func(health *mountproto.RuntimeHealthResponse) { health.NativeMount.Source = "other" }},
		{name: "root read epoch", mutate: func(health *mountproto.RuntimeHealthResponse) { health.NativeMount.RootReadEpoch = 0 }},
		{name: "broker topology", mutate: func(health *mountproto.RuntimeHealthResponse) { health.BrokerPhase = mountproto.BrokerPhaseLive }},
	} {
		t.Run(test.name, func(t *testing.T) {
			health := exact
			native := *exact.NativeMount
			health.NativeMount = &native
			test.mutate(&health)
			if err := validateProvisionRuntime(build, presentationRoot, health); err == nil {
				t.Fatal("inexact runtime was accepted")
			}
		})
	}
}

func TestRepositoryMountPreparationRequiresExactTypedProof(t *testing.T) {
	presentationRoot := filepath.Join(t.TempDir(), "mount")
	provision, err := NewRepositoryProvision(presentationRoot, filepath.Join(t.TempDir(), "repository"))
	if err != nil {
		t.Fatal(err)
	}
	const activation = "activation-1"
	response := exactMountPreparationResponse(provision, activation)
	if err := validateRepositoryMountPreparation(provision, activation, response); err != nil {
		t.Fatalf("validate exact proof: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*catalogproto.TenantPreparationProof)
	}{
		{name: "kind", mutate: func(proof *catalogproto.TenantPreparationProof) {
			proof.Presentation.Kind = catalogproto.PresentationKindFileProvider
		}},
		{name: "missing mount", mutate: func(proof *catalogproto.TenantPreparationProof) {
			proof.Presentation.Mount = nil
		}},
		{name: "foreign shape", mutate: func(proof *catalogproto.TenantPreparationProof) {
			proof.Presentation.FileProvider = &catalogproto.FileProviderPresentationProof{}
		}},
		{name: "catalog tenant", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.Catalog.Tenant = "other" }},
		{name: "catalog generation", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.Catalog.Generation++ }},
		{name: "requested revision", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.Catalog.Requested = 0 }},
		{name: "desired revision", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.Catalog.Desired++ }},
		{name: "observed revision", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.Catalog.Observed++ }},
		{name: "verified revision", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.Catalog.Verified++ }},
		{name: "applied revision", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.Catalog.Applied++ }},
		{name: "mount tenant", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.Presentation.Mount.TenantID = "other" }},
		{name: "mount generation", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.Presentation.Mount.Generation++ }},
		{name: "public path", mutate: func(proof *catalogproto.TenantPreparationProof) {
			proof.Presentation.Mount.PublicPath = filepath.Join(t.TempDir(), "other")
		}},
		{name: "activation", mutate: func(proof *catalogproto.TenantPreparationProof) {
			proof.Presentation.Mount.ActivationGeneration = "other"
		}},
		{name: "source authority", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.SourceAuthority = "other" }},
		{name: "source revision", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.SourceRevision = 0 }},
		{name: "catalog revision", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.CatalogRevision++ }},
		{name: "change id", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.ChangeID = "" }},
		{name: "operation id", mutate: func(proof *catalogproto.TenantPreparationProof) { proof.OperationID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := clonePreparationResponse(response)
			test.mutate(candidate.Proof)
			if err := validateRepositoryMountPreparation(provision, activation, candidate); err == nil {
				t.Fatal("inexact mount proof was accepted")
			}
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*catalogproto.PrepareTenantResponse)
	}{
		{name: "response protocol", mutate: func(response *catalogproto.PrepareTenantResponse) { response.Protocol++ }},
		{name: "response code", mutate: func(response *catalogproto.PrepareTenantResponse) { response.Code = catalogproto.ErrorCodeUnavailable }},
		{name: "response message", mutate: func(response *catalogproto.PrepareTenantResponse) { response.Message = "unexpected" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := clonePreparationResponse(response)
			test.mutate(&candidate)
			if err := validateRepositoryMountPreparation(provision, activation, candidate); err == nil {
				t.Fatal("inexact preparation response was accepted")
			}
		})
	}
	if err := validateRepositoryMountPreparation(
		provision, activation, catalogproto.PrepareTenantResponse{},
	); err == nil {
		t.Fatal("missing mount proof was accepted")
	}
}

func exactMountPreparationResponse(
	provision RepositoryProvision,
	activation string,
) catalogproto.PrepareTenantResponse {
	const revision = 4
	return catalogproto.PrepareTenantResponse{
		Protocol: catalogproto.Version, Code: catalogproto.ErrorCodeOk,
		Proof: &catalogproto.TenantPreparationProof{
			Catalog: catalogproto.CatalogLaneProof{
				Tenant: catalogproto.TenantID(provision.Tenant.ID), Generation: uint64(provision.Tenant.Generation),
				Requested: revision, Desired: revision, Observed: revision, Verified: revision, Applied: revision,
			},
			Presentation: catalogproto.PresentationProof{
				Kind: catalogproto.PresentationKindMount,
				Mount: &catalogproto.MountPresentationProof{
					TenantID: catalogproto.TenantID(provision.Tenant.ID), Generation: uint64(provision.Tenant.Generation),
					PublicPath: provision.Definition.Mount.PresentationRoot, ActivationGeneration: activation,
				},
			},
			SourceAuthority: catalogproto.SourceAuthorityID(provision.Tenant.Authority),
			SourceRevision:  3, CatalogRevision: revision,
			ChangeID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
}

func clonePreparationResponse(response catalogproto.PrepareTenantResponse) catalogproto.PrepareTenantResponse {
	cloned := response
	proof := *response.Proof
	presentation := proof.Presentation
	mount := *presentation.Mount
	presentation.Mount = &mount
	proof.Presentation = presentation
	cloned.Proof = &proof
	return cloned
}

func TestMergeDesiredDeclarationIsOrderedExactAndNonDestructive(t *testing.T) {
	first := protocolDeclarationForTest("cc-notes:first", "one")
	third := protocolDeclarationForTest("cc-notes:third", "three")
	second := protocolDeclarationForTest("cc-notes:second", "two")
	current := []catalogproto.SourceAuthorityDeclaration{first, third}
	merged, changed, err := mergeDesiredDeclaration(current, second)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !reflect.DeepEqual(merged, []catalogproto.SourceAuthorityDeclaration{first, second, third}) {
		t.Fatalf("merged = %+v changed=%t", merged, changed)
	}
	if !reflect.DeepEqual(current, []catalogproto.SourceAuthorityDeclaration{first, third}) {
		t.Fatalf("input mutated = %+v", current)
	}
	idempotent, changed, err := mergeDesiredDeclaration(merged, second)
	if err != nil || changed || !reflect.DeepEqual(idempotent, merged) {
		t.Fatalf("idempotent merge = %+v changed=%t err=%v", idempotent, changed, err)
	}
	conflict := second
	conflict.DriverConfig = []byte("different")
	if _, _, err := mergeDesiredDeclaration(merged, conflict); err == nil {
		t.Fatal("conflicting immutable declaration was accepted")
	}
}

func TestProtocolFleetStateBindsEveryExactDeclaration(t *testing.T) {
	declarations := []catalogproto.SourceAuthorityDeclaration{
		protocolDeclarationForTest("cc-notes:first", "one"),
		protocolDeclarationForTest("cc-notes:second", "two"),
	}
	state := protocolFleetStateForTest(t, declarations)
	if err := validateProtocolFleetState(state, declarations); err != nil {
		t.Fatal(err)
	}
	tampered := append([]catalogproto.SourceAuthorityDeclaration(nil), declarations...)
	tampered[1].DriverConfig = []byte("different")
	if err := validateProtocolFleetState(state, tampered); err == nil {
		t.Fatal("fleet state accepted different declarations")
	}
}

func protocolDeclarationForTest(authority, config string) catalogproto.SourceAuthorityDeclaration {
	return catalogproto.SourceAuthorityDeclaration{
		Authority: catalogproto.SourceAuthorityID(authority), DriverID: gitDriverID,
		DriverConfig: []byte(config), DeclarationDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func protocolFleetStateForTest(
	t *testing.T,
	declarations []catalogproto.SourceAuthorityDeclaration,
) catalogproto.DesiredSourceFleetState {
	t.Helper()
	authorities := make([]causal.SourceAuthorityID, len(declarations))
	catalogDeclarations := make([]catalog.SourceAuthorityDeclaration, len(declarations))
	for index, declaration := range declarations {
		authorities[index] = causal.SourceAuthorityID(declaration.Authority)
		catalogDeclarations[index] = catalog.SourceAuthorityDeclaration{
			Authority: authorities[index], DriverID: declaration.DriverID,
			DriverConfig: append([]byte(nil), declaration.DriverConfig...),
		}
		digest, err := hex.DecodeString(declaration.DeclarationDigest)
		if err != nil {
			t.Fatal(err)
		}
		copy(catalogDeclarations[index].DeclarationDigest[:], digest)
	}
	authoritiesDigest, err := catalog.SourceAuthorityFleetDigest(authorities)
	if err != nil {
		t.Fatal(err)
	}
	declarationsDigest, err := catalog.SourceAuthorityFleetDeclarationsDigest(catalogDeclarations)
	if err != nil {
		t.Fatal(err)
	}
	return catalogproto.DesiredSourceFleetState{
		Owner: string(helperOwner), Generation: 1, AuthorityCount: uint64(len(declarations)),
		AuthoritiesDigest:  hex.EncodeToString(authoritiesDigest[:]),
		DeclarationsDigest: hex.EncodeToString(declarationsDigest[:]),
	}
}
