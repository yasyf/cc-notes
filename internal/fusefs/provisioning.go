package fusefs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/tenant"
)

const repositoryGeneration catalog.Generation = 1

// RepositoryProvision is cc-notes' complete immutable v1 repository declaration.
type RepositoryProvision struct {
	Tenant      Tenant
	Spec        tenant.TenantSpec
	Declaration catalog.SourceAuthorityDeclaration
}

// NewRepositoryProvision derives one exact repository identity and desired topology declaration.
func NewRepositoryProvision(presentationRoot, repoRoot string) (RepositoryProvision, error) {
	if !exactAbsolutePath(presentationRoot) || !exactAbsolutePath(repoRoot) {
		return RepositoryProvision{}, errors.New("cc-notes provision: exact absolute roots are required")
	}
	digest := sha256.Sum256([]byte("cc-notes.repository.v1\x00" + repoRoot))
	encoded := hex.EncodeToString(digest[:])
	tenantID, err := catalog.NewTenantID("cc-notes-" + encoded)
	if err != nil {
		return RepositoryProvision{}, err
	}
	routeName := "repo-" + encoded[:16]
	productTenant := Tenant{
		ID: tenantID, Generation: repositoryGeneration, Authority: AuthorityForTenant(tenantID),
		RouteName: routeName, RepoRoot: repoRoot,
	}
	if err := productTenant.Validate(); err != nil {
		return RepositoryProvision{}, err
	}
	declaration, err := newGitDriverDeclaration(productTenant.Authority, repoRoot)
	if err != nil {
		return RepositoryProvision{}, err
	}
	return RepositoryProvision{
		Tenant: productTenant,
		Spec: tenant.TenantSpec{
			OwnerID: helperOwner, ID: productTenant.ID,
			Mount:   tenant.MountSpec{PresentationRoot: filepath.Join(presentationRoot, routeName)},
			Backing: tenant.BackingSpec{Root: repoRoot}, Content: tenant.ContentSource{ID: string(productTenant.Authority)},
			Traits: tenant.TenantTraits{
				Access: tenant.ReadWrite, CaseSensitivity: catalog.CaseSensitive,
				Presentations: catalog.PresentMount,
			},
			Generation: repositoryGeneration,
		},
		Declaration: declaration,
	}, nil
}
