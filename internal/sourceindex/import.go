package sourceindex

import (
	"context"
	"crypto/sha256"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

// ImportProvenance binds an issued revision's observable descriptors to authenticated evidence.
type ImportProvenance struct {
	Evidence     string
	RenderPolicy string
	ObjectDigest [sha256.Size]byte
}

// ImportPolicy supplies independently captured historical byte provenance during explicit import.
type ImportPolicy interface {
	SourceProvenance(context.Context, model.SHA, gitobj.SourceManifest) (ImportProvenance, error)
}
