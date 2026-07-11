package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// prefill carries the flag values `add --checkout` seeds a new-entity buffer
// with: the optional TITLE positional plus the anchor and tag flags.
// noteAdapter ignores when (notes have no trigger).
type prefill struct {
	title    string
	when     string
	tags     []string
	commits  []string
	paths    []string
	dirs     []string
	branches []string
}

// editAdapter binds the file-edit engine to one entity kind: how to load a
// snapshot and its head, render it to an editable buffer, build the new-entity
// template from a prefill, and turn an edited buffer back into ops. doc and note
// share the engine; only these closures differ.
type editAdapter struct {
	kind       model.Kind
	load       func(ctx context.Context, s *store.Store, prefix string) (model.Snapshot, model.SHA, error)
	render     func(model.Snapshot) []byte
	template   func(prefill) []byte
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
		kind: model.KindDoc,
		load: func(ctx context.Context, s *store.Store, prefix string) (model.Snapshot, model.SHA, error) {
			_, d, err := docSpec.load(ctx, s, prefix)
			if err != nil {
				return nil, "", err
			}
			return d, d.Head, nil
		},
		render: func(snap model.Snapshot) []byte { return fusefs.RenderDoc(snap.(model.Doc)) },
		template: func(p prefill) []byte {
			return fusefs.NewDocTemplate(p.title, p.when, p.tags, buildAnchors(p.commits, p.paths, p.dirs, p.branches))
		},
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
			return bornVerify(ctx, s, refs.For(model.KindDoc, snap.EntityID()), snap.(model.Doc).Anchors)
		},
		print: func(cmd *cobra.Command, s *store.Store, snap model.Snapshot, jsonOut bool) error {
			return printDoc(cmd, s, snap.(model.Doc), "", jsonOut)
		},
	}
}

func noteAdapter() editAdapter {
	return editAdapter{
		kind: model.KindNote,
		load: func(ctx context.Context, s *store.Store, prefix string) (model.Snapshot, model.SHA, error) {
			_, n, err := noteSpec.load(ctx, s, prefix)
			if err != nil {
				return nil, "", err
			}
			return n, n.Head, nil
		},
		render: func(snap model.Snapshot) []byte { return fusefs.RenderNote(snap.(model.Note)) },
		template: func(p prefill) []byte {
			return fusefs.NewNoteTemplate(p.title, p.tags, buildAnchors(p.commits, p.paths, p.dirs, p.branches))
		},
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
			return bornVerify(ctx, s, refs.For(model.KindNote, snap.EntityID()), snap.(model.Note).Anchors)
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

// fileModeOpts carries a file-mode request's inputs beyond the mode flags: the
// prefill an add --checkout seeds its buffer with and the attach paths an add
// --apply rides on the create. edit and abort branches read neither.
type fileModeOpts struct {
	checkout bool
	apply    bool
	abort    bool
	jsonOut  bool
	prefill  prefill
	attach   []string
}

// runFileMode handles a --checkout/--apply/--abort request for an add (isAdd)
// or edit command. The three flags are mutually exclusive; each branch permits
// only the content flags fileModeAllowed lists and rejects the rest. For edit,
// args[0] is the entity id prefix; for add --apply/--abort it is the buffer path
// printed at checkout; add --checkout takes an optional TITLE. It is only called
// when one of the three flags is set.
func runFileMode(cmd *cobra.Command, a editAdapter, isAdd bool, args []string, opts fileModeOpts) error {
	mode, err := soleMode(opts.checkout, opts.apply, opts.abort)
	if err != nil {
		return err
	}
	if changed := changedContentFlags(cmd, fileModeAllowed(isAdd, opts.checkout, opts.apply)...); len(changed) > 0 {
		return &UsageError{Err: fmt.Errorf("--%s cannot be combined with content flags: --%s", mode, strings.Join(changed, ", --"))}
	}
	ctx := cmd.Context()
	s, err := openStore()
	if err != nil {
		return err
	}
	switch {
	case isAdd && opts.checkout:
		return addCheckout(ctx, cmd, s, a, opts.prefill)
	case isAdd && opts.apply:
		return addApply(ctx, cmd, s, a, args[0], opts.attach, opts.jsonOut)
	case isAdd:
		return abortFiles(cmd, filesForPath(args[0]))
	case opts.checkout:
		return editCheckout(ctx, cmd, s, a, args[0])
	case opts.apply:
		return editApply(ctx, cmd, s, a, args[0], opts.jsonOut)
	default:
		return editAbort(ctx, cmd, s, a, args[0])
	}
}

// fileModeAllowed lists the content flags a file-mode branch permits: an add
// --checkout seeds its buffer from the anchor/label flags (and when, doc-only),
// an add --apply may carry --attach, and every edit branch plus add --abort
// permits none.
func fileModeAllowed(isAdd, checkout, apply bool) []string {
	switch {
	case isAdd && checkout:
		return []string{"when", "label", "commit", "path", "dir", "branch"}
	case isAdd && apply:
		return []string{"attach"}
	default:
		return nil
	}
}

// optionalTitle returns the sole optional TITLE an add --checkout may carry, or
// "" when the user gave none.
func optionalTitle(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
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

// changedContentFlags returns the names of the entity-content flags the user set
// that are not in allowed, so file mode can reject combining the wrong flags
// with --checkout/--apply/--abort. The three mode flags and --json are never
// content flags, so all four are always ignored.
func changedContentFlags(cmd *cobra.Command, allowed ...string) []string {
	var names []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		switch f.Name {
		case "checkout", "apply", "abort", "json":
			return
		}
		if slices.Contains(allowed, f.Name) {
			return
		}
		names = append(names, f.Name)
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
	ops, err = resolveOpCommitAnchors(ctx, s.Git, ops)
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

func addCheckout(ctx context.Context, cmd *cobra.Command, s *store.Store, a editAdapter, p prefill) error {
	if p.title != "" {
		if err := validateTitle(p.title, bufferHint); err != nil {
			return err
		}
	}
	commits, err := resolveCommits(ctx, s.Git, p.commits)
	if err != nil {
		return err
	}
	p.commits = commits
	dir, err := editDir(ctx, s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create edit dir: %w", err)
	}
	files := bufferFiles(dir, "new-"+model.NewNonce())
	if err := writeBuffer(files, a.template(p), editMeta{Kind: a.noun(), New: true}); err != nil {
		return err
	}
	return announceCheckout(cmd, files.buffer, fmt.Sprintf("cc-notes %s add --apply %s", a.noun(), files.buffer))
}

func addApply(ctx context.Context, cmd *cobra.Command, s *store.Store, a editAdapter, path string, attach []string, jsonOut bool) error {
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
	ops, err = resolveOpCommitAnchors(ctx, s.Git, ops)
	if err != nil {
		return fmt.Errorf("%w\nfix %s and re-run --apply, or --abort to discard", err, files.buffer)
	}
	if err := autoInstall(ctx, cmd, s.Git); err != nil {
		return err
	}
	attOps, err := attachOps(ctx, cmd, s, attach)
	if err != nil {
		return err
	}
	snap, err := createEntity(ctx, cmd, s, append(ops, attOps...))
	if err != nil {
		return err
	}
	// A file-mode add re-asserts the fact now, so a dedupe hit re-verifies the
	// reused entity rather than skipping it: bornVerify appends a VerifyNote that
	// refreshes the survivor's witness and verified_at/by (fold.foldNote/foldDoc),
	// exactly as a fresh add. The dedupe scan excludes stale twins, so the
	// survivor here is live.
	snap, err = a.bornVerify(ctx, s, snap)
	if err != nil {
		return err
	}
	removeBuffer(files)
	return a.print(cmd, s, snap, jsonOut)
}

// resolveOpCommitAnchors rewrites every commit-anchor value in ops to its full
// 40-char sha via resolveCommits, so a short sha hand-typed into a buffer's
// commits: lands canonicalized like the flag path does. After canonicalizing it
// cancels AddAnchor/RemoveAnchor pairs that name the same anchor — those are
// spurious diffs from a buffer that only re-spelled a sha (a short prefix diffs
// as add-short + remove-full, which without this would net to a dropped anchor),
// not real edits. Surviving ops keep their order.
func resolveOpCommitAnchors(ctx context.Context, g gitcmd.Git, ops []model.Op) ([]model.Op, error) {
	resolve := func(a model.Anchor) (model.Anchor, error) {
		if a.Kind != model.AnchorCommit {
			return a, nil
		}
		full, err := resolveCommits(ctx, g, []string{a.Value})
		if err != nil {
			return a, err
		}
		a.Value = full[0]
		return a, nil
	}
	for i := range ops {
		switch o := ops[i].(type) {
		case model.CreateDoc:
			if err := resolveAnchorsInPlace(o.Anchors, resolve); err != nil {
				return nil, err
			}
		case model.CreateNote:
			if err := resolveAnchorsInPlace(o.Anchors, resolve); err != nil {
				return nil, err
			}
		case model.AddAnchor:
			r, err := resolve(o.Anchor)
			if err != nil {
				return nil, err
			}
			ops[i] = model.AddAnchor{Anchor: r}
		case model.RemoveAnchor:
			r, err := resolve(o.Anchor)
			if err != nil {
				return nil, err
			}
			ops[i] = model.RemoveAnchor{Anchor: r}
		}
	}
	return cancelAnchorPairs(ops), nil
}

func resolveAnchorsInPlace(anchors []model.Anchor, resolve func(model.Anchor) (model.Anchor, error)) error {
	for j, a := range anchors {
		r, err := resolve(a)
		if err != nil {
			return err
		}
		anchors[j] = r
	}
	return nil
}

// cancelAnchorPairs drops every AddAnchor whose exact anchor is also removed in
// ops, and every matching RemoveAnchor, leaving the unpaired ops in order.
func cancelAnchorPairs(ops []model.Op) []model.Op {
	added := map[model.Anchor]bool{}
	removed := map[model.Anchor]bool{}
	for _, op := range ops {
		switch o := op.(type) {
		case model.AddAnchor:
			added[o.Anchor] = true
		case model.RemoveAnchor:
			removed[o.Anchor] = true
		}
	}
	spurious := func(a model.Anchor) bool { return added[a] && removed[a] }
	out := make([]model.Op, 0, len(ops))
	for _, op := range ops {
		switch o := op.(type) {
		case model.AddAnchor:
			if spurious(o.Anchor) {
				continue
			}
		case model.RemoveAnchor:
			if spurious(o.Anchor) {
				continue
			}
		}
		out = append(out, op)
	}
	return out
}

func abortFiles(cmd *cobra.Command, files editFiles) error {
	if _, err := os.Stat(files.meta); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: no edit buffer at %s", store.ErrNotFound, files.buffer)
	}
	removeBuffer(files)
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "discarded edit buffer %s\n", files.buffer)
	return err
}
