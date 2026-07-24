package fusefs

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/holder"
)

func TestRepositoryProvisionRequiresExactLocalProof(t *testing.T) {
	provision, err := NewRepositoryProvision(
		filepath.Join(t.TempDir(), "mount"), filepath.Join(t.TempDir(), "repository"),
	)
	if err != nil {
		t.Fatal(err)
	}
	const activation = "activation-1"
	exact := exactLocalProvisionProof(t, provision, activation)
	if err := validateRepositoryProvision(provision, activation, exact); err != nil {
		t.Fatalf("validate exact proof: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*holder.LocalProvisionProof)
	}{
		{name: "fleet owner", mutate: func(proof *holder.LocalProvisionProof) { proof.Fleet.Owner = "other" }},
		{name: "fleet count", mutate: func(proof *holder.LocalProvisionProof) { proof.Fleet.AuthorityCount = 0 }},
		{name: "tenant", mutate: func(proof *holder.LocalProvisionProof) { proof.Tenant.Tenant = "other" }},
		{name: "generation", mutate: func(proof *holder.LocalProvisionProof) { proof.Tenant.Generation++ }},
		{name: "presentation set", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Tenant.Presentations = catalog.PresentFileProvider
		}},
		{name: "catalog tenant", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.Catalog.Tenant = "other"
		}},
		{name: "requested revision", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.Catalog.Requested = 0
		}},
		{name: "applied revision", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.Catalog.Applied++
		}},
		{name: "kind", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.Presentation.Kind = catalogproto.PresentationKindFileProvider
		}},
		{name: "missing mount", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.Presentation.Mount = nil
		}},
		{name: "public path", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.Presentation.Mount.PublicPath = filepath.Join(t.TempDir(), "other")
		}},
		{name: "activation", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.Presentation.Mount.ActivationGeneration = "other"
		}},
		{name: "source authority", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.SourceAuthority = "other"
		}},
		{name: "source publication", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.SourcePublication = ""
		}},
		{name: "source revision", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.SourceRevision = 0
		}},
		{name: "catalog revision", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.CatalogRevision++
		}},
		{name: "change", mutate: func(proof *holder.LocalProvisionProof) { proof.Preparation.ChangeID = "" }},
		{name: "operation", mutate: func(proof *holder.LocalProvisionProof) { proof.Preparation.OperationID = "" }},
		{name: "critical proof", mutate: func(proof *holder.LocalProvisionProof) {
			proof.Preparation.CriticalReadiness = &catalogproto.CriticalReadinessProof{}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := exact
			if exact.Preparation.Presentation.Mount != nil {
				mount := *exact.Preparation.Presentation.Mount
				candidate.Preparation.Presentation.Mount = &mount
			}
			test.mutate(&candidate)
			if err := validateRepositoryProvision(provision, activation, candidate); err == nil {
				t.Fatal("inexact local proof was accepted")
			}
		})
	}
}

func exactLocalProvisionProof(
	t *testing.T,
	provision RepositoryProvision,
	activation string,
) holder.LocalProvisionProof {
	t.Helper()
	authorities := []causal.SourceAuthorityID{provision.Declaration.Authority}
	authoritiesDigest, err := catalog.SourceAuthorityFleetDigest(authorities)
	if err != nil {
		t.Fatal(err)
	}
	declarationsDigest, err := catalog.SourceAuthorityFleetDeclarationsDigest(
		[]catalog.SourceAuthorityDeclaration{provision.Declaration},
	)
	if err != nil {
		t.Fatal(err)
	}
	return holder.LocalProvisionProof{
		Fleet: catalog.DesiredSourceAuthorityFleetState{
			Owner: catalog.SourceAuthorityFleetOwnerID(helperOwner), Generation: 1, AuthorityCount: 1,
			AuthoritiesDigest: authoritiesDigest, DeclarationsDigest: declarationsDigest,
		},
		Tenant: holder.LocalTenantAcknowledgement{
			Tenant: provision.Spec.ID, Generation: provision.Spec.Generation, Presentations: catalog.PresentMount,
		},
		Preparation: catalogproto.TenantPreparationProof{
			Catalog: catalogproto.CatalogLaneProof{
				Tenant: catalogproto.TenantID(provision.Spec.ID), Generation: uint64(provision.Spec.Generation),
				Requested: 7, Desired: 7, Observed: 7, Verified: 7, Applied: 7,
			},
			Presentation: catalogproto.PresentationProof{
				Kind: catalogproto.PresentationKindMount,
				Mount: &catalogproto.MountPresentationProof{
					TenantID: catalogproto.TenantID(provision.Spec.ID), Generation: uint64(provision.Spec.Generation),
					PublicPath: provision.Spec.Mount.PresentationRoot, ActivationGeneration: activation,
				},
			},
			SourceAuthority:   catalogproto.SourceAuthorityID(provision.Declaration.Authority),
			SourcePublication: catalogproto.OperationID(causalID(1)), SourceRevision: 4, CatalogRevision: 7,
			ChangeID: catalogproto.ChangeID(causalID(2)), OperationID: catalogproto.OperationID(causalID(3)),
		},
	}
}

func causalID(value int) string { return fmt.Sprintf("%032x", value) }
