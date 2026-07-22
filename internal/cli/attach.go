package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// attachOps ingests every --attach file into the local LFS store — fully
// offline; content reaches the remote only at sync — and returns one
// AddAttachment op per file. A duplicate base name within one invocation, a
// missing file, an empty file, or an invalid name is a UsageError. The one
// time ingestion installs the prune guard it announces the config line on
// stderr, mirroring autoInstall.
func attachOps(ctx context.Context, cmd *cobra.Command, s *store.Store, paths []string) ([]model.Op, error) {
	seen := make(map[string]bool, len(paths))
	ops := make([]model.Op, 0, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		if seen[name] {
			return nil, &UsageError{Err: fmt.Errorf("--attach %s: duplicate attachment name %q in one invocation", path, name)}
		}
		seen[name] = true
		att, guarded, err := s.AttachFile(ctx, path)
		if errors.Is(err, model.ErrInvalidValue) || errors.Is(err, os.ErrNotExist) {
			return nil, &UsageError{Err: err}
		}
		if err != nil {
			return nil, err
		}
		if guarded {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: installed %s in .git/config (makes `git lfs prune` keep objects it cannot verify on the remote)\n",
				strings.Join(store.PruneGuardConfigs[:], " and ")); err != nil {
				return nil, err
			}
		}
		ops = append(ops, model.AddAttachment(att))
	}
	return ops, nil
}

// checkAttachCollisions rejects an --attach whose base name collides with a
// live attachment: replacing content silently would orphan the old bytes
// behind the same name, so the caller must opt in with --replace.
func checkAttachCollisions(live []model.Attachment, paths []string) error {
	names := make(map[string]bool, len(live))
	for _, a := range live {
		names[a.Name] = true
	}
	for _, p := range paths {
		if name := filepath.Base(p); names[name] {
			return &UsageError{Err: fmt.Errorf("attachment %q already exists; pass --replace to overwrite it", name)}
		}
	}
	return nil
}

// entityAttachments renders an entity's attachments with their local
// presence, always non-nil so JSON serializes an empty list rather than
// null. The LFS store is opened only when there is something to probe, so
// attachment-less output paths cost nothing.
func entityAttachments(ctx context.Context, s *store.Store, atts []model.Attachment) ([]attachmentDTO, error) {
	out := make([]attachmentDTO, 0, len(atts))
	if len(atts) == 0 {
		return out, nil
	}
	content := s.LFS()
	for _, a := range atts {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out = append(out, attachmentDTO{Name: a.Name, OID: a.OID, Size: a.Size, Present: content.Has(a.OID)})
	}
	return out, nil
}

// readScript reads the validation script file at path and returns its
// contents verbatim. The contents become a criterion's check command, run only
// by task validate.
func readScript(path string) (string, error) {
	//nolint:gosec // G304: path is the operator-supplied validation-script file for this CLI; reading it is the intended behavior.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read script %s: %w", path, err)
	}
	return string(data), nil
}
