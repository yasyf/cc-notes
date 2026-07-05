package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func newAttachmentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attachment",
		Short: "Read attachment content from the local git-lfs store",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newAttachmentGetCmd(),
		newAttachmentPathCmd(),
	)
	return cmd
}

func newAttachmentGetCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "get ID NAME",
		Short: "Stream an attachment's content to stdout (or -o PATH)",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			att, content, err := lookupAttachment(ctx, s, args[0], args[1])
			if err != nil {
				return err
			}
			f, err := openAttachmentContent(content, att)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			dest := cmd.OutOrStdout()
			if output != "" {
				//nolint:gosec // G304: output is the CLI's own -o output target for attachment get, taken by design.
				out, cerr := os.Create(output)
				if cerr != nil {
					return fmt.Errorf("write %s: %w", output, cerr)
				}
				// A write-side close can surface a deferred flush failure
				// (full disk, failed fsync); fold it into the result when the
				// copy itself succeeded, so a truncated -o file never exits 0.
				defer func() {
					if closeErr := out.Close(); closeErr != nil && err == nil {
						err = fmt.Errorf("write %s: %w", output, closeErr)
					}
				}()
				dest = out
			}
			if _, err = io.Copy(dest, f); err != nil {
				return fmt.Errorf("read attachment %s: %w", att.Name, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to PATH instead of stdout")
	return cmd
}

func newAttachmentPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path ID NAME",
		Short: "Print an attachment's absolute object path for zero-copy reads",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			att, content, err := lookupAttachment(ctx, s, args[0], args[1])
			if err != nil {
				return err
			}
			if !content.Has(att.OID) {
				return missingContentError(att)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), content.Path(att.OID))
			return err
		},
	}
}

// lookupAttachment resolves an id prefix across the note, doc, and log
// namespaces, folds the entity, and returns the named attachment plus the
// repository's local LFS store. A prefix matching more than one kind is
// ambiguous; an unknown name is not-found and lists what the entity carries.
func lookupAttachment(ctx context.Context, s *store.Store, prefix, name string) (model.Attachment, lfs.Store, error) {
	ref, err := resolveAttachable(ctx, s, prefix)
	if err != nil {
		return model.Attachment{}, lfs.Store{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return model.Attachment{}, lfs.Store{}, err
	}
	atts := snapshotAttachments(snapshot)
	for _, a := range atts {
		if a.Name == name {
			content, err := s.LFS(ctx)
			if err != nil {
				return model.Attachment{}, lfs.Store{}, err
			}
			return a, content, nil
		}
	}
	names := make([]string, len(atts))
	for i, a := range atts {
		names[i] = a.Name
	}
	return model.Attachment{}, lfs.Store{}, fmt.Errorf("%w: no attachment %q on %s (has: %s)",
		store.ErrNotFound, name, snapshot.EntityID().Short(), csvOrDash(names))
}

// resolveAttachable expands an id prefix against the three attachment-bearing
// namespaces: note, doc, and log. Ids are globally unique, so more than one
// kind matching is ambiguous.
func resolveAttachable(ctx context.Context, s *store.Store, prefix string) (string, error) {
	matched := make([]string, 0, 3)
	for _, kind := range []refs.Kind{refs.KindNote, refs.KindDoc, refs.KindLog} {
		ref, err := s.Resolve(ctx, kind, prefix)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		matched = append(matched, ref)
	}
	switch len(matched) {
	case 0:
		return "", fmt.Errorf("%w: no note, doc, or log matches %q", store.ErrNotFound, prefix)
	case 1:
		return matched[0], nil
	default:
		return "", ambiguousAcrossKinds(ctx, s, prefix, matched)
	}
}

// snapshotAttachments returns the attachment set of a note, doc, or log
// snapshot; resolveAttachable only yields those kinds.
func snapshotAttachments(snapshot model.Snapshot) []model.Attachment {
	switch v := snapshot.(type) {
	case model.Note:
		return v.Attachments
	case model.Doc:
		return v.Attachments
	case model.Log:
		return v.Attachments
	default:
		panic(fmt.Sprintf("attachment lookup on non-attachable snapshot %T", snapshot))
	}
}

// openAttachmentContent opens att's bytes in the local LFS store, mapping a
// missing object to the sync remediation.
func openAttachmentContent(content lfs.Store, att model.Attachment) (*os.File, error) {
	f, err := content.Open(att.OID)
	if errors.Is(err, lfs.ErrObjectMissing) {
		return nil, missingContentError(att)
	}
	if err != nil {
		return nil, fmt.Errorf("open attachment %s: %w", att.Name, err)
	}
	return f, nil
}

// missingContentError names a referenced-but-absent attachment and the
// command that fetches it.
func missingContentError(att model.Attachment) error {
	return fmt.Errorf("attachment %q (oid %s) is not present locally; run `cc-notes sync` to download it", att.Name, att.OID)
}
