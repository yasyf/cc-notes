package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// docTemplate and noteTemplate are the empty new-entity buffers that
// add --checkout writes: only the editable frontmatter keys, then an empty
// body. They omit id, author, and created on purpose — those keys would make
// the file parse as an existing entity, which NewDoc/NewNote reject. The
// leading and trailing --- match the frontmatter RenderDoc/RenderNote produce,
// so the template round-trips through ParseDoc/ParseNote.
const (
	docTemplate  = "---\ntitle: \"\"\nwhen: \"\"\ntags: []\n---\n"
	noteTemplate = "---\ntitle: \"\"\ntags: []\n---\n"
)

// editAdapter binds the file-edit engine to one entity kind: how to load a
// snapshot and its head, render it to an editable buffer, build the new-entity
// template, and turn an edited buffer back into ops. doc and note share the
// engine; only these closures differ.
type editAdapter struct {
	kind       refs.Kind
	load       func(ctx context.Context, s *store.Store, prefix string) (model.Snapshot, model.SHA, error)
	render     func(model.Snapshot) []byte
	template   func() []byte
	diffOps    func(base model.Snapshot, data []byte) ([]model.Op, error)
	createOps  func(data []byte) ([]model.Op, error)
	bornVerify func(ctx context.Context, s *store.Store, snap model.Snapshot) (model.Snapshot, error)
	print      func(cmd *cobra.Command, s *store.Store, snap model.Snapshot, jsonOut bool) error
}

// noun is the entity word used in messages; for the cc-notes kinds it equals
// the kind string ("doc", "note").
func (a editAdapter) noun() string { return string(a.kind) }

func docAdapter() editAdapter {
	return editAdapter{
		kind: refs.KindDoc,
		load: func(ctx context.Context, s *store.Store, prefix string) (model.Snapshot, model.SHA, error) {
			_, d, err := loadDoc(ctx, s, prefix)
			if err != nil {
				return nil, "", err
			}
			return d, d.Head, nil
		},
		render:   func(snap model.Snapshot) []byte { return fusefs.RenderDoc(snap.(model.Doc)) },
		template: func() []byte { return []byte(docTemplate) },
		diffOps: func(base model.Snapshot, data []byte) ([]model.Op, error) {
			p, err := fusefs.ParseDoc(data)
			if err != nil {
				return nil, err
			}
			ops, err := fusefs.DiffDoc(base.(model.Doc), p)
			if err != nil {
				return nil, err
			}
			if err := validateOpTitles(ops); err != nil {
				return nil, err
			}
			for _, op := range ops {
				if sb, ok := op.(model.SetBody); ok && sb.Body == "" {
					return nil, errEmptyDocBody(bufferHint)
				}
			}
			return ops, nil
		},
		createOps: func(data []byte) ([]model.Op, error) {
			p, err := fusefs.ParseDoc(data)
			if err != nil {
				return nil, err
			}
			ops, err := fusefs.NewDoc(p)
			if err != nil {
				return nil, err
			}
			if err := validateOpTitles(ops); err != nil {
				return nil, err
			}
			if ops[0].(model.CreateDoc).Body == "" {
				return nil, errEmptyDocBody(bufferHint)
			}
			return ops, nil
		},
		bornVerify: func(ctx context.Context, s *store.Store, snap model.Snapshot) (model.Snapshot, error) {
			return bornVerify(ctx, s, refs.Doc(snap.EntityID()), snap.(model.Doc).Anchors)
		},
		print: func(cmd *cobra.Command, s *store.Store, snap model.Snapshot, jsonOut bool) error {
			return printDoc(cmd, s, snap.(model.Doc), "", jsonOut)
		},
	}
}

func noteAdapter() editAdapter {
	return editAdapter{
		kind: refs.KindNote,
		load: func(ctx context.Context, s *store.Store, prefix string) (model.Snapshot, model.SHA, error) {
			_, n, err := loadNote(ctx, s, prefix)
			if err != nil {
				return nil, "", err
			}
			return n, n.Head, nil
		},
		render:   func(snap model.Snapshot) []byte { return fusefs.RenderNote(snap.(model.Note)) },
		template: func() []byte { return []byte(noteTemplate) },
		diffOps: func(base model.Snapshot, data []byte) ([]model.Op, error) {
			p, err := fusefs.ParseNote(data)
			if err != nil {
				return nil, err
			}
			ops, err := fusefs.DiffNote(base.(model.Note), p)
			if err != nil {
				return nil, err
			}
			if err := validateOpTitles(ops); err != nil {
				return nil, err
			}
			return ops, nil
		},
		createOps: func(data []byte) ([]model.Op, error) {
			p, err := fusefs.ParseNote(data)
			if err != nil {
				return nil, err
			}
			ops, err := fusefs.NewNote(p)
			if err != nil {
				return nil, err
			}
			if err := validateOpTitles(ops); err != nil {
				return nil, err
			}
			return ops, nil
		},
		bornVerify: func(ctx context.Context, s *store.Store, snap model.Snapshot) (model.Snapshot, error) {
			return bornVerify(ctx, s, refs.Note(snap.EntityID()), snap.(model.Note).Anchors)
		},
		print: func(cmd *cobra.Command, s *store.Store, snap model.Snapshot, jsonOut bool) error {
			return printNote(cmd, s, snap.(model.Note), jsonOut)
		},
	}
}

// validateOpTitles applies the title cap to every create-or-rename op, so a
// file-mode --apply is guarded exactly like the flag-mode add/edit RunE.
func validateOpTitles(ops []model.Op) error {
	for _, op := range ops {
		var title string
		switch o := op.(type) {
		case model.CreateNote:
			title = o.Title
		case model.CreateDoc:
			title = o.Title
		case model.SetTitle:
			title = o.Title
		default:
			continue
		}
		if err := validateTitle(title, bufferHint); err != nil {
			return err
		}
	}
	return nil
}

// bornVerify appends the verify_note op a freshly created note or doc carries,
// matching `note add` / `doc add`: it witnesses the entity's anchors against
// the current HEAD so a file-created entity is born verified, not unverified.
func bornVerify(ctx context.Context, s *store.Store, ref string, anchors []model.Anchor) (model.Snapshot, error) {
	head, err := resolveHead(ctx, s)
	if err != nil {
		return nil, err
	}
	witness, err := buildWitness(ctx, s, head, anchors)
	if err != nil {
		return nil, err
	}
	return s.Append(ctx, ref, []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}})
}

// editMeta is the sidecar JSON written next to an edit buffer. It records the
// entity kind, whether the buffer creates a new entity, and — for an edit — the
// head sha the buffer was rendered from, so --apply diffs against that exact
// base. It lives under the git common dir, never on refs/cc-notes/*.
type editMeta struct {
	Kind string `json:"kind"`
	New  bool   `json:"new"`
	Base string `json:"base,omitempty"`
}

// editFiles is the buffer and its sidecar for one checked-out entity.
type editFiles struct {
	buffer string
	meta   string
}

// editDir is the per-repo directory holding edit buffers: a sibling of the
// fold cache under the git common dir, so it is shared across worktrees,
// rebuildable, and never pushed.
func editDir(ctx context.Context, s *store.Store) (string, error) {
	common, err := s.Git.CommonDir(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "cc-notes", "edit"), nil
}

func bufferFiles(dir, stem string) editFiles {
	buf := filepath.Join(dir, stem+".md")
	return editFiles{buffer: buf, meta: buf + ".meta"}
}

// filesForPath maps a buffer path passed to add --apply/--abort back to its
// buffer and sidecar.
func filesForPath(path string) editFiles {
	return editFiles{buffer: path, meta: path + ".meta"}
}

func writeBuffer(files editFiles, body []byte, meta editMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(files.buffer, body, 0o600); err != nil {
		return fmt.Errorf("write edit buffer: %w", err)
	}
	if err := os.WriteFile(files.meta, data, 0o600); err != nil {
		return fmt.Errorf("write edit metadata: %w", err)
	}
	return nil
}

func readBuffer(files editFiles) (editMeta, []byte, error) {
	//nolint:gosec // G304: files.meta is this repo's own edit-buffer sidecar under the git common dir, not external input.
	metaData, err := os.ReadFile(files.meta)
	if errors.Is(err, os.ErrNotExist) {
		return editMeta{}, nil, fmt.Errorf("%w: no edit buffer at %s; run --checkout first", store.ErrNotFound, files.buffer)
	}
	if err != nil {
		return editMeta{}, nil, fmt.Errorf("read edit metadata: %w", err)
	}
	var meta editMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return editMeta{}, nil, fmt.Errorf("parse edit metadata %s: %w", files.meta, err)
	}
	//nolint:gosec // G304: files.buffer is this repo's own edit buffer under the git common dir, not external input.
	body, err := os.ReadFile(files.buffer)
	if err != nil {
		return editMeta{}, nil, fmt.Errorf("read edit buffer %s: %w", files.buffer, err)
	}
	return meta, body, nil
}

func removeBuffer(files editFiles) {
	_ = os.Remove(files.buffer)
	_ = os.Remove(files.meta)
}

// announceCheckout writes the buffer path alone to stdout — so it is scriptable
// via command substitution — and the apply hint to stderr.
func announceCheckout(cmd *cobra.Command, buffer, applyCmd string) error {
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), buffer); err != nil {
		return err
	}
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "edit it, then run: %s\n", applyCmd)
	return err
}

// runFileMode handles a --checkout/--apply/--abort request for an add (isAdd)
// or edit command. The three flags are mutually exclusive and cannot be
// combined with content flags. For edit, args[0] is the entity id prefix; for
// add --apply/--abort it is the buffer path printed at checkout; add --checkout
// takes no positional. It is only called when one of the three flags is set.
func runFileMode(cmd *cobra.Command, a editAdapter, isAdd bool, args []string, checkout, apply, abort, jsonOut bool) error {
	mode, err := soleMode(checkout, apply, abort)
	if err != nil {
		return err
	}
	if changed := changedContentFlags(cmd); len(changed) > 0 {
		return &UsageError{Err: fmt.Errorf("--%s cannot be combined with content flags: --%s", mode, strings.Join(changed, ", --"))}
	}
	ctx := cmd.Context()
	s, err := openStore()
	if err != nil {
		return err
	}
	switch {
	case isAdd && checkout:
		return addCheckout(ctx, cmd, s, a)
	case isAdd && apply:
		return addApply(ctx, cmd, s, a, args[0], jsonOut)
	case isAdd:
		return abortFiles(cmd, filesForPath(args[0]))
	case checkout:
		return editCheckout(ctx, cmd, s, a, args[0])
	case apply:
		return editApply(ctx, cmd, s, a, args[0], jsonOut)
	default:
		return editAbort(ctx, cmd, s, a, args[0])
	}
}

// soleMode names the single set file-mode flag, or fails if more than one is
// set. The caller guarantees at least one is set.
func soleMode(checkout, apply, abort bool) (string, error) {
	n, mode := 0, ""
	if checkout {
		n, mode = n+1, "checkout"
	}
	if apply {
		n, mode = n+1, "apply"
	}
	if abort {
		n, mode = n+1, "abort"
	}
	if n > 1 {
		return "", &UsageError{Err: errors.New("--checkout, --apply, and --abort are mutually exclusive")}
	}
	return mode, nil
}

// changedContentFlags returns the names of the entity-content flags the user
// set, so file mode can reject combining them with --checkout/--apply/--abort.
// --json travels with --apply and is not a content flag.
func changedContentFlags(cmd *cobra.Command) []string {
	var names []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		switch f.Name {
		case "checkout", "apply", "abort", "json":
		default:
			names = append(names, f.Name)
		}
	})
	return names
}

// resolveID expands an id prefix to the full entity id of its ref.
func resolveID(ctx context.Context, s *store.Store, a editAdapter, prefix string) (string, error) {
	ref, err := s.Resolve(ctx, a.kind, prefix)
	if err != nil {
		return "", err
	}
	parsed, err := refs.Parse(ref)
	if err != nil {
		return "", err
	}
	return string(parsed.ID), nil
}

func editCheckout(ctx context.Context, cmd *cobra.Command, s *store.Store, a editAdapter, prefix string) error {
	snap, head, err := a.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	dir, err := editDir(ctx, s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create edit dir: %w", err)
	}
	files := bufferFiles(dir, string(snap.EntityID()))
	if _, err := os.Stat(files.buffer); err == nil {
		return fmt.Errorf("edit buffer already exists at %s; --apply it or --abort to discard", files.buffer)
	}
	if err := writeBuffer(files, a.render(snap), editMeta{Kind: a.noun(), Base: string(head)}); err != nil {
		return err
	}
	return announceCheckout(cmd, files.buffer, fmt.Sprintf("cc-notes %s edit %s --apply", a.noun(), prefix))
}

func editApply(ctx context.Context, cmd *cobra.Command, s *store.Store, a editAdapter, prefix string, jsonOut bool) error {
	ref, err := s.Resolve(ctx, a.kind, prefix)
	if err != nil {
		return err
	}
	parsed, err := refs.Parse(ref)
	if err != nil {
		return err
	}
	dir, err := editDir(ctx, s)
	if err != nil {
		return err
	}
	files := bufferFiles(dir, string(parsed.ID))
	meta, data, err := readBuffer(files)
	if err != nil {
		return err
	}
	if meta.Kind != a.noun() || meta.New {
		return fmt.Errorf("%s is not a %s edit buffer", files.buffer, a.noun())
	}
	base, err := s.LoadAt(ctx, model.SHA(meta.Base))
	if err != nil {
		return err
	}
	ops, err := a.diffOps(base, data)
	if err != nil {
		return fmt.Errorf("%w\nfix %s and re-run --apply, or --abort to discard", err, files.buffer)
	}
	if len(ops) == 0 {
		removeBuffer(files)
		snap, err := s.Load(ctx, ref)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "no changes to apply"); err != nil {
			return err
		}
		return a.print(cmd, s, snap, jsonOut)
	}
	if err := autoInstall(ctx, cmd, s.Git); err != nil {
		return err
	}
	snap, err := s.Append(ctx, ref, ops)
	if err != nil {
		return err
	}
	removeBuffer(files)
	return a.print(cmd, s, snap, jsonOut)
}

func editAbort(ctx context.Context, cmd *cobra.Command, s *store.Store, a editAdapter, prefix string) error {
	id, err := resolveID(ctx, s, a, prefix)
	if err != nil {
		return err
	}
	dir, err := editDir(ctx, s)
	if err != nil {
		return err
	}
	return abortFiles(cmd, bufferFiles(dir, id))
}

func addCheckout(ctx context.Context, cmd *cobra.Command, s *store.Store, a editAdapter) error {
	dir, err := editDir(ctx, s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create edit dir: %w", err)
	}
	files := bufferFiles(dir, "new-"+model.NewNonce())
	if err := writeBuffer(files, a.template(), editMeta{Kind: a.noun(), New: true}); err != nil {
		return err
	}
	return announceCheckout(cmd, files.buffer, fmt.Sprintf("cc-notes %s add --apply %s", a.noun(), files.buffer))
}

func addApply(ctx context.Context, cmd *cobra.Command, s *store.Store, a editAdapter, path string, jsonOut bool) error {
	files := filesForPath(path)
	meta, data, err := readBuffer(files)
	if err != nil {
		return err
	}
	if meta.Kind != a.noun() || !meta.New {
		return fmt.Errorf("%s is not a %s add buffer", files.buffer, a.noun())
	}
	ops, err := a.createOps(data)
	if err != nil {
		return fmt.Errorf("%w\nfix %s and re-run --apply, or --abort to discard", err, files.buffer)
	}
	if err := autoInstall(ctx, cmd, s.Git); err != nil {
		return err
	}
	snap, err := s.Create(ctx, ops)
	if err != nil {
		return err
	}
	verified, err := a.bornVerify(ctx, s, snap)
	if err != nil {
		return err
	}
	removeBuffer(files)
	return a.print(cmd, s, verified, jsonOut)
}

func abortFiles(cmd *cobra.Command, files editFiles) error {
	if _, err := os.Stat(files.meta); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: no edit buffer at %s", store.ErrNotFound, files.buffer)
	}
	removeBuffer(files)
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "discarded edit buffer %s\n", files.buffer)
	return err
}
