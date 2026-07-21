package fusefs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/mountproto"
)

const repositoryGeneration catalog.Generation = 1

// RepositoryProvision is cc-notes' complete immutable v1 repository declaration.
type RepositoryProvision struct {
	Tenant      Tenant
	Definition  mountproto.TenantDefinition
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
	tenant := Tenant{
		ID: tenantID, Generation: repositoryGeneration, Authority: AuthorityForTenant(tenantID),
		RouteName: routeName, RepoRoot: repoRoot,
	}
	if err := tenant.Validate(); err != nil {
		return RepositoryProvision{}, err
	}
	declaration, err := newGitDriverDeclaration(tenant.Authority, repoRoot)
	if err != nil {
		return RepositoryProvision{}, err
	}
	return RepositoryProvision{
		Tenant: tenant,
		Definition: mountproto.TenantDefinition{
			PresentationRoot: filepath.Join(presentationRoot, routeName),
			BackingRoot:      repoRoot, ContentSourceID: string(tenant.Authority),
			AccessMode: mountproto.AccessModeReadWrite, CasePolicy: mountproto.CasePolicySensitive,
			Presentations: []mountproto.Presentation{mountproto.PresentationMount},
			Generation:    uint64(repositoryGeneration),
		},
		Declaration: declaration,
	}, nil
}
