// Package helpercontract defines cc-notes' exact helper business protocol.
package helpercontract

import (
	"errors"
	"path/filepath"
)

const (
	// ProvisionRepositoryOperation is the sole v1 repository provisioning operation.
	ProvisionRepositoryOperation = "product.cc-notes.repository.provision.v1"
	// ProvisionSchema is the hard-cut business payload schema.
	ProvisionSchema uint16 = 1
	// MaxProvisionPayload bounds either serialized provision message. A
	// repository root is bounded by the platform path limit and the rest of
	// each envelope is fixed, so this sits far above both.
	MaxProvisionPayload = 64 << 10
)

// ProvisionRepositoryRequest identifies one canonical repository root.
type ProvisionRepositoryRequest struct {
	Schema         uint16 `json:"schema"`
	RepositoryRoot string `json:"repository_root"`
}

// Validate rejects any non-v1 or path-ambiguous request.
func (r ProvisionRepositoryRequest) Validate() error {
	if r.Schema != ProvisionSchema {
		return errors.New("cc-notes helper: repository provision schema is not v1")
	}
	if !filepath.IsAbs(r.RepositoryRoot) || filepath.Clean(r.RepositoryRoot) != r.RepositoryRoot {
		return errors.New("cc-notes helper: repository root is not exact and absolute")
	}
	return nil
}

// ProvisionRepositoryResponse proves one exact tenant generation was prepared.
type ProvisionRepositoryResponse struct {
	Schema     uint16 `json:"schema"`
	Tenant     string `json:"tenant"`
	Generation uint64 `json:"generation"`
}

// Validate rejects any incomplete or non-v1 response.
func (r ProvisionRepositoryResponse) Validate() error {
	if r.Schema != ProvisionSchema || r.Tenant == "" || r.Generation == 0 {
		return errors.New("cc-notes helper: repository provision response is not exact v1")
	}
	return nil
}
