package fusefs

import (
	"context"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/holder"
)

// ProvisionRepositoryLocal publishes and prepares one repository through the
// callback-scoped immutable-owner controller.
func ProvisionRepositoryLocal(
	ctx context.Context,
	controller *holder.LocalTenantController,
	plan holder.RuntimePlan,
	repoRoot string,
) (helpercontract.ProvisionRepositoryResponse, error) {
	if controller == nil {
		return helpercontract.ProvisionRepositoryResponse{}, errors.New("cc-notes provision: local tenant controller is nil")
	}
	readiness, err := controller.Readiness(ctx)
	if err != nil {
		return helpercontract.ProvisionRepositoryResponse{}, fmt.Errorf("cc-notes provision: observe admitted runtime: %w", err)
	}
	if readiness.RuntimeBuild != plan.BuildID() || readiness.ProcessGeneration == (proc.OwnerGeneration{}) ||
		readiness.ActivationGeneration == "" {
		return helpercontract.ProvisionRepositoryResponse{}, errors.New("cc-notes provision: admitted runtime identity is not exact")
	}
	provision, err := NewRepositoryProvision(plan.Paths().PresentationRoot, repoRoot)
	if err != nil {
		return helpercontract.ProvisionRepositoryResponse{}, err
	}
	proof, err := controller.ProvisionAndPrepare(ctx, holder.LocalProvisionRequest{
		Declaration: provision.Declaration,
		Tenant:      provision.Spec,
		Preparation: holder.LocalPreparationRequest{
			Generation: provision.Spec.Generation, Presentation: catalog.PresentationMount,
		},
	})
	if err != nil {
		return helpercontract.ProvisionRepositoryResponse{}, fmt.Errorf("cc-notes provision: provision and prepare repository tenant: %w", err)
	}
	if err := validateRepositoryProvision(provision, readiness.ActivationGeneration, proof); err != nil {
		return helpercontract.ProvisionRepositoryResponse{}, err
	}
	return helpercontract.ProvisionRepositoryResponse{
		Schema: helpercontract.ProvisionSchema, Tenant: string(provision.Spec.ID),
		Generation: uint64(provision.Spec.Generation),
	}, nil
}

func validateRepositoryProvision(
	provision RepositoryProvision,
	activationGeneration string,
	proof holder.LocalProvisionProof,
) error {
	if err := proof.Fleet.Validate(); err != nil {
		return fmt.Errorf("cc-notes provision: desired source fleet proof is invalid: %w", err)
	}
	if proof.Fleet.Owner != catalog.SourceAuthorityFleetOwnerID(helperOwner) || proof.Fleet.AuthorityCount == 0 ||
		proof.Tenant.Tenant != provision.Spec.ID || proof.Tenant.Generation != provision.Spec.Generation ||
		proof.Tenant.Presentations != catalog.PresentMount {
		return errors.New("cc-notes provision: durable repository proof is not exact")
	}
	prepared := proof.Preparation
	if err := catalogproto.Validate(prepared); err != nil {
		return fmt.Errorf("cc-notes provision: mount preparation proof is invalid: %w", err)
	}
	catalogProof := prepared.Catalog
	mount := prepared.Presentation.Mount
	if prepared.Presentation.Kind != catalogproto.PresentationKindMount || mount == nil ||
		prepared.Presentation.FileProvider != nil || catalogProof.Tenant != catalogproto.TenantID(provision.Spec.ID) ||
		catalogProof.Generation != uint64(provision.Spec.Generation) || catalogProof.Requested == 0 ||
		catalogProof.Desired != catalogProof.Requested || catalogProof.Observed != catalogProof.Requested ||
		catalogProof.Verified != catalogProof.Requested || catalogProof.Applied != catalogProof.Requested ||
		mount.TenantID != catalogproto.TenantID(provision.Spec.ID) || mount.Generation != uint64(provision.Spec.Generation) ||
		mount.PublicPath != provision.Spec.Mount.PresentationRoot || mount.ActivationGeneration != activationGeneration ||
		prepared.SourceAuthority != catalogproto.SourceAuthorityID(provision.Spec.Content.ID) ||
		prepared.SourcePublication == "" || prepared.SourceRevision == 0 ||
		prepared.CatalogRevision != catalogProof.Requested || prepared.ChangeID == "" || prepared.OperationID == "" ||
		prepared.CriticalReadiness != nil {
		return errors.New("cc-notes provision: mount preparation proof is not exact")
	}
	return nil
}
