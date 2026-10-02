package notes_test

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

const (
	matrixTarget    = "svc/handler.go"
	unrelatedTarget = "elsewhere/unrelated.go"
)

const countingGitScript = "#!/bin/sh\nprintf '%s\\037' \"$@\" >> \"$CC_NOTES_GIT_TRACE\"\nprintf '\\n' >> \"$CC_NOTES_GIT_TRACE\"\nexec \"$CC_NOTES_REAL_GIT\" \"$@\"\n"

const racingGitScript = `#!/bin/sh
printf '%s\037' "$@" >> "$CC_NOTES_GIT_TRACE"
printf '\n' >> "$CC_NOTES_GIT_TRACE"
"$CC_NOTES_REAL_GIT" "$@"
rc=$?
enumerates=0
entityRefs=0
scoring=0
for a in "$@"; do
	case "$a" in
	for-each-ref) enumerates=1 ;;
	refs/cc-notes/*) entityRefs=1 ;;
	log) scoring=1 ;;
	esac
done
state=$(cat "$CC_NOTES_RACE_STATE")
if [ "$state" = listing ] && [ $enumerates = 1 ] && [ $entityRefs = 1 ]; then
	if "$CC_NOTES_REAL_GIT" -C "$CC_NOTES_RACE_DIR" update-ref "$CC_NOTES_RACE_REF" "$CC_NOTES_RACE_B" "$CC_NOTES_RACE_A"; then
		printf '%s' "$CC_NOTES_RACE_NEXT" > "$CC_NOTES_RACE_STATE"
	else
		printf failed > "$CC_NOTES_RACE_STATE"
	fi
elif [ "$state" = scoring ] && [ $scoring = 1 ]; then
	if "$CC_NOTES_REAL_GIT" -C "$CC_NOTES_RACE_DIR" update-ref "$CC_NOTES_RACE_REF" "$CC_NOTES_RACE_A" "$CC_NOTES_RACE_B"; then
		printf done > "$CC_NOTES_RACE_STATE"
	else
		printf failed > "$CC_NOTES_RACE_STATE"
	fi
fi
exit $rc
`

const rootRaceGitScript = `#!/bin/sh
printf '%s\037' "$@" >> "$CC_NOTES_GIT_TRACE"
printf '\n' >> "$CC_NOTES_GIT_TRACE"
hit=0
for a in "$@"; do
	[ "$a" = "$CC_NOTES_RACE_TRIGGER" ] && hit=1
done
if [ "$(cat "$CC_NOTES_RACE_STATE")" != armed ] || [ $hit = 0 ]; then
	exec "$CC_NOTES_REAL_GIT" "$@"
fi
case "$CC_NOTES_RACE_MODE" in
orig-head) "$CC_NOTES_REAL_GIT" -C "$CC_NOTES_RACE_DIR" update-ref ORIG_HEAD "$CC_NOTES_RACE_VALUE" ;;
include)
	[ -z "$CC_NOTES_RACE_LINK" ] || ln -sfn "$CC_NOTES_RACE_LINK_SWAP" "$CC_NOTES_RACE_LINK"
	[ -z "$CC_NOTES_RACE_MKDIR" ] || mkdir "$CC_NOTES_RACE_MKDIR"
	"$CC_NOTES_REAL_GIT" config --file "$CC_NOTES_RACE_FILE" user.email "$CC_NOTES_RACE_VALUE" ;;
esac
"$CC_NOTES_REAL_GIT" "$@"
rc=$?
case "$CC_NOTES_RACE_MODE" in
orig-head) "$CC_NOTES_REAL_GIT" -C "$CC_NOTES_RACE_DIR" update-ref -d ORIG_HEAD ;;
include)
	rm "$CC_NOTES_RACE_FILE"
	[ -z "$CC_NOTES_RACE_MKDIR" ] || rmdir "$CC_NOTES_RACE_MKDIR"
	[ -z "$CC_NOTES_RACE_LINK" ] || ln -sfn "$CC_NOTES_RACE_LINK_RESTORE" "$CC_NOTES_RACE_LINK" ;;
esac
printf fired > "$CC_NOTES_RACE_STATE"
exit $rc
`

type cacheTier int

const (
	tierHit cacheTier = iota + 1
	tierRevalidate
	tierRebuild
)

func (k cacheTier) String() string {
	switch k {
	case tierHit:
		return "tier 1 (stamps only)"
	case tierRevalidate:
		return "tier 2 (git, no render)"
	case tierRebuild:
		return "tier 3 (render)"
	default:
		return fmt.Sprintf("tier(%d)", int(k))
	}
}

type gitCounter struct{ trace string }

func installCountingGit(t *testing.T, script string) gitCounter {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath git: %v", err)
	}
	g := gitCounter{trace: filepath.Join(t.TempDir(), "git.log")}
	t.Setenv("CC_NOTES_REAL_GIT", realGit)
	t.Setenv("CC_NOTES_GIT_TRACE", g.trace)
	g.prepend(t, script)
	return g
}

func (g gitCounter) prepend(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (g gitCounter) calls(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(g.trace)
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read git trace: %v", err)
	}
	return bytes.Count(data, []byte("\n"))
}

type tierProbe struct {
	t       *testing.T
	c       *notes.Client
	dir     string
	filter  notes.RelevantFilter
	git     gitCounter
	renders int
	last    []byte
}

func (p *tierProbe) render(entries []notes.RelevantEntry) ([]byte, error) {
	p.renders++
	return json.Marshal(entries)
}

func (p *tierProbe) call(step string) cacheTier {
	p.t.Helper()
	calls, renders := p.git.calls(p.t), p.renders
	out, err := p.c.RelevantCached(p.t.Context(), matrixTarget, p.filter, "json", p.render)
	if err != nil {
		p.t.Fatalf("%s: RelevantCached: %v", step, err)
	}
	p.last = out
	switch {
	case p.renders > renders:
		return tierRebuild
	case p.git.calls(p.t) > calls:
		return tierRevalidate
	default:
		return tierHit
	}
}

func (p *tierProbe) expect(step string, want cacheTier) {
	p.t.Helper()
	if got := p.call(step); got != want {
		p.t.Fatalf("%s %+v: served by %v, want %v", step, p.filter, got, want)
	}
	if fresh := freshRelevantJSON(p.t, p.dir, p.filter); !bytes.Equal(p.last, fresh) {
		p.t.Fatalf("%s %+v: cached answer differs from a fresh Relevant\ncached %s\nfresh  %s", step, p.filter, p.last, fresh)
	}
}

func (p *tierProbe) promoteThen(step string, want cacheTier) {
	p.t.Helper()
	p.call(step + " (promotion)")
	p.expect(step, want)
}

func (p *tierProbe) entry(step string) notes.RelevantCacheProbe {
	p.t.Helper()
	probe, ok, err := notes.RelevantCacheProbeOf(p.c, matrixTarget, p.filter, "json")
	if err != nil {
		p.t.Fatalf("%s: RelevantCacheProbeOf: %v", step, err)
	}
	if !ok {
		p.t.Fatalf("%s: no cache entry persisted", step)
	}
	return probe
}

func freshRelevantJSON(t *testing.T, dir string, filter notes.RelevantFilter) []byte {
	t.Helper()
	c, err := notes.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	entries, err := c.Relevant(t.Context(), matrixTarget, filter)
	if err != nil {
		t.Fatalf("Relevant: %v", err)
	}
	out, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func seedNotes(tb testing.TB, dir string, n, relevant int, branch func(i int) string) {
	tb.Helper()
	s, err := store.Open(dir)
	if err != nil {
		tb.Fatalf("store.Open: %v", err)
	}
	updates := make([]gitcmd.RefUpdate, n)
	for i := range n {
		anchors := []model.Anchor{{Kind: model.AnchorPath, Value: unrelatedTarget}}
		if i < relevant {
			anchors = []model.Anchor{{Kind: model.AnchorPath, Value: matrixTarget}}
		}
		if b := branch(i); b != "" {
			anchors = append(anchors, model.Anchor{Kind: model.AnchorBranch, Value: b})
		}
		prepared, err := s.PrepareCreateExact(tb.Context(), []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: fmt.Sprintf("note %04d", i), Anchors: anchors}})
		if err != nil {
			tb.Fatalf("PrepareCreateExact %d: %v", i, err)
		}
		updates[i] = prepared.RefUpdate()
	}
	if err := s.Git.UpdateRefs(tb.Context(), updates); err != nil {
		tb.Fatalf("UpdateRefs: %v", err)
	}
}

type warmLayout func(tb testing.TB, dir string, root model.SHA, n, relevant int) (anchored []string)

func cyclingLayout(anchors, extra []string) warmLayout {
	return func(tb testing.TB, dir string, root model.SHA, n, relevant int) []string {
		tb.Helper()
		createBranches(tb, dir, root, slices.Concat(anchors, extra))
		seedNotes(tb, dir, n, relevant, func(i int) string {
			if i >= relevant {
				return ""
			}
			return anchors[i%len(anchors)]
		})
		return anchors[:min(len(anchors), relevant)]
	}
}

func diverseLayout(prefixes []string) warmLayout {
	return func(tb testing.TB, dir string, root model.SHA, n, relevant int) []string {
		tb.Helper()
		names := make([]string, n)
		for i := range names {
			names[i] = fmt.Sprintf("d%04d", i)
			if len(prefixes) > 0 {
				names[i] = prefixes[i%len(prefixes)] + "/" + names[i]
			}
		}
		createBranches(tb, dir, root, names[:relevant])
		createBranches(tb, dir, orphanCommit(tb, dir, root), names[relevant:])
		seedNotes(tb, dir, n, relevant, func(i int) string { return names[i] })
		return names
	}
}

func orphanCommit(tb testing.TB, dir string, tree model.SHA) model.SHA {
	tb.Helper()
	return model.SHA(gittest.Git(tb, dir, "commit-tree", string(tree)+"^{tree}", "-m", "orphan"))
}

func createBranches(tb testing.TB, dir string, at model.SHA, names []string) {
	tb.Helper()
	var updates strings.Builder
	for _, name := range names {
		fmt.Fprintf(&updates, "create refs/heads/%s %s\n", name, at)
	}
	cmd := exec.CommandContext(tb.Context(), "git", "-C", dir, "update-ref", "--stdin")
	cmd.Stdin = strings.NewReader(updates.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("create branches: %v\n%s", err, out)
	}
}

type matrixFixture struct {
	repo    string
	dir     string
	c       *notes.Client
	note    model.EntityID
	sibling model.EntityID
	root    model.SHA
	theirs  model.SHA
	counter gitCounter
}

func newMatrixFixture(t *testing.T) *matrixFixture {
	t.Helper()
	c, dir := newClient(t)
	root := commitFile(t, dir, matrixTarget, "v1\n")
	note := makeNote(t, c, "handler", notes.AnchorSpec{Paths: []string{matrixTarget}})
	return &matrixFixture{repo: dir, dir: dir, c: c, note: note, root: root}
}

func (fx *matrixFixture) run(t *testing.T, args ...string) string {
	t.Helper()
	return gittest.Git(t, fx.repo, args...)
}

func (fx *matrixFixture) noteRef() string {
	return refs.For(model.KindNote, fx.note)
}

func (fx *matrixFixture) commonDir(t *testing.T) string {
	t.Helper()
	_, common := gittest.Dirs(t, fx.repo)
	return common
}

func (fx *matrixFixture) second(t *testing.T) *notes.Client {
	t.Helper()
	c, err := notes.Open(fx.repo)
	if err != nil {
		t.Fatalf("Open second client: %v", err)
	}
	return c
}

func (fx *matrixFixture) crossAuthor(t *testing.T) {
	t.Helper()
	fx.run(t, "checkout", "-q", "-b", "work")
	fx.theirs = commitFileAs(t, fx.repo, relevantOther, matrixTarget, "theirs\n")
}

func (fx *matrixFixture) detachAhead(t *testing.T) {
	t.Helper()
	fx.run(t, "checkout", "-q", "--detach")
	fx.theirs = commitFile(t, fx.repo, "svc/ahead.go", "ahead\n")
}

func (fx *matrixFixture) sideCommit(t *testing.T) model.SHA {
	t.Helper()
	return model.SHA(fx.run(t, "commit-tree", string(fx.root)+"^{tree}", "-m", "side"))
}

func (fx *matrixFixture) anchored(t *testing.T, title string, branches ...string) {
	t.Helper()
	makeNote(t, fx.c, title, notes.AnchorSpec{Paths: []string{matrixTarget}, Branches: branches})
}

func (fx *matrixFixture) includeExtra(t *testing.T, email string) {
	t.Helper()
	fx.run(t, "config", "include.path", "extra.config")
	fx.run(t, "config", "-f", filepath.Join(fx.commonDir(t), "extra.config"), "user.email", email)
}

func (fx *matrixFixture) symlinkRef(t *testing.T, name, target string, at model.SHA) {
	t.Helper()
	fx.run(t, "config", "core.preferSymlinkRefs", "true")
	fx.run(t, "update-ref", target, string(at))
	fx.run(t, "symbolic-ref", name, target)
	if link, err := os.Readlink(filepath.Join(fx.commonDir(t), filepath.FromSlash(name))); err != nil || link != target {
		t.Fatalf("%s is not a symlink to %s: %q, %v", name, target, link, err)
	}
}

func (fx *matrixFixture) armRootRace(t *testing.T, mode, trigger, value, file string) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "race")
	if err := os.WriteFile(state, []byte("armed"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CC_NOTES_RACE_STATE", state)
	t.Setenv("CC_NOTES_RACE_DIR", fx.repo)
	t.Setenv("CC_NOTES_RACE_MODE", mode)
	t.Setenv("CC_NOTES_RACE_TRIGGER", trigger)
	t.Setenv("CC_NOTES_RACE_VALUE", value)
	t.Setenv("CC_NOTES_RACE_FILE", file)
	fx.counter = installCountingGit(t, rootRaceGitScript)
}

func (fx *matrixFixture) rootRaceFired(t *testing.T) {
	t.Helper()
	if got, err := os.ReadFile(os.Getenv("CC_NOTES_RACE_STATE")); err != nil || string(got) != "fired" {
		t.Fatalf("race state = %q, %v; want fired (the shim never saw its trigger)", got, err)
	}
	if _, err := os.Lstat(os.Getenv("CC_NOTES_RACE_FILE")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s survived the race: %v", os.Getenv("CC_NOTES_RACE_FILE"), err)
	}
}

func gitWrite(args ...string) func(*testing.T, *matrixFixture) {
	return func(t *testing.T, fx *matrixFixture) {
		t.Helper()
		fx.run(t, args...)
	}
}

type matrixWrite struct {
	name    string
	do      func(*testing.T, *matrixFixture)
	want    cacheTier
	changes bool
}

type matrixCase struct {
	name    string
	filter  notes.RelevantFilter
	setup   func(*testing.T, *matrixFixture)
	writes  []matrixWrite
	settled cacheTier
}

func TestRelevantCachedInvalidatesOnExternalWrites(t *testing.T) {
	for _, tc := range invalidationCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(notes.SetRelevantRacyWindow(0))
			fx := newMatrixFixture(t)
			if tc.setup != nil {
				tc.setup(t, fx)
			}
			fx.counter = installCountingGit(t, countingGitScript)
			p := &tierProbe{t: t, c: fx.c, dir: fx.dir, filter: tc.filter, git: fx.counter}
			p.expect("cold", tierRebuild)
			p.promoteThen("warm", tierHit)
			settled := cmp.Or(tc.settled, tierHit)
			for _, w := range tc.writes {
				before := p.last
				w.do(t, fx)
				p.expect(w.name, w.want)
				if changed := !bytes.Equal(before, p.last); changed != w.changes {
					t.Fatalf("%s: output changed = %t, want %t\nbefore %s\nafter  %s", w.name, changed, w.changes, before, p.last)
				}
				p.promoteThen(w.name+", settled", settled)
			}
		})
	}
}

func invalidationCases() []matrixCase {
	return []matrixCase{
		{
			name: "entity created by a second client",
			writes: []matrixWrite{{name: "second client creates a note", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				if _, _, err := fx.second(t).CreateNote(t.Context(), notes.NoteSpec{Title: "from elsewhere", Anchors: notes.AnchorSpec{Paths: []string{matrixTarget}}}); err != nil {
					t.Fatalf("CreateNote: %v", err)
				}
			}}},
		},
		{
			name: "entity edited by a second client",
			writes: []matrixWrite{{name: "second client retitles the note", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				title := "handler, revised elsewhere"
				if _, err := fx.second(t).EditNote(t.Context(), fx.note, notes.NoteEdit{Title: &title}); err != nil {
					t.Fatalf("EditNote: %v", err)
				}
			}}},
		},
		{
			name: "loose entity deleted with its namespace directory pruned",
			writes: []matrixWrite{{name: "update-ref -d prunes refs/cc-notes/notes", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", "-d", fx.noteRef())
				if _, err := os.Stat(filepath.Join(fx.commonDir(t), "refs", "cc-notes", "notes")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("namespace directory survived the delete: %v", err)
				}
			}}},
		},
		{
			name:   "refs packed",
			writes: []matrixWrite{{name: "pack-refs --all", do: gitWrite("pack-refs", "--all"), want: tierRevalidate}},
		},
		{
			name:  "packed entity edited",
			setup: func(t *testing.T, fx *matrixFixture) { fx.run(t, "pack-refs", "--all") },
			writes: []matrixWrite{{name: "second client edits a packed entity", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				title := "handler, unpacked"
				if _, err := fx.second(t).EditNote(t.Context(), fx.note, notes.NoteEdit{Title: &title}); err != nil {
					t.Fatalf("EditNote: %v", err)
				}
			}}},
		},
		{
			name:  "packed entity deleted",
			setup: func(t *testing.T, fx *matrixFixture) { fx.run(t, "pack-refs", "--all") },
			writes: []matrixWrite{{name: "update-ref -d rewrites packed-refs", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", "-d", fx.noteRef())
			}}},
		},
		{
			name: "no-op entity update",
			writes: []matrixWrite{{name: "update-ref to the current tip", want: tierRevalidate, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", fx.noteRef(), fx.run(t, "rev-parse", fx.noteRef()))
			}}},
		},
		{
			name: "entity ref turned into a symref",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.sibling = makeNote(t, fx.c, "sibling", notes.AnchorSpec{Paths: []string{matrixTarget}})
			},
			writes: []matrixWrite{{name: "symbolic-ref over the entity ref", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "symbolic-ref", fx.noteRef(), refs.For(model.KindNote, fx.sibling))
			}}},
		},
		{
			name: "anchored branch moved",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", "refs/heads/feature", string(fx.sideCommit(t)))
				fx.anchored(t, "feature work", "feature")
			},
			writes: []matrixWrite{{name: "feature moved onto HEAD's history", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", "refs/heads/feature", string(fx.root))
			}}},
		},
		{
			name: "unrelated flat branch created",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "branch", "feature")
				fx.anchored(t, "feature work", "feature")
			},
			writes: []matrixWrite{{name: "git branch unrelated", do: gitWrite("branch", "unrelated"), want: tierRevalidate}},
		},
		{
			name: "unrelated nested branch created",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "branch", "team/feature")
				fx.anchored(t, "team work", "team/feature")
			},
			writes: []matrixWrite{{name: "git branch team/unrelated", do: gitWrite("branch", "team/unrelated"), want: tierRevalidate}},
		},
		{
			name: "detached HEAD gains a nearer bookmark",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.detachAhead(t)
				fx.anchored(t, "trunk work", "main")
			},
			writes: []matrixWrite{{name: "unanchored bookmark created at HEAD", do: gitWrite("branch", "wip"), want: tierRebuild, changes: true}},
		},
		{
			name: "detached bookmark symref retargeted",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.detachAhead(t)
				fx.run(t, "update-ref", "refs/remotes/side/tip", string(fx.theirs))
				fx.run(t, "symbolic-ref", "refs/heads/alias", "refs/heads/main")
				fx.anchored(t, "trunk work", "main")
			},
			writes: []matrixWrite{{name: "alias retargeted at HEAD", do: gitWrite("symbolic-ref", "refs/heads/alias", "refs/remotes/side/tip"), want: tierRebuild, changes: true}},
		},
		{
			name: "detached bookmark symref target moved",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.detachAhead(t)
				fx.run(t, "update-ref", "refs/remotes/side/tip", string(fx.root))
				fx.run(t, "symbolic-ref", "refs/heads/alias", "refs/remotes/side/tip")
				fx.anchored(t, "trunk work", "main")
			},
			writes: []matrixWrite{{name: "symref target outside refs/heads moved to HEAD", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", "refs/remotes/side/tip", string(fx.theirs))
			}}},
		},
		{
			name: "symlink alias target moved",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.symlinkRef(t, "refs/heads/alias", "refs/remotes/upstream/tip", fx.root)
				fx.anchored(t, "alias work", "alias")
			},
			writes: []matrixWrite{{name: "upstream/tip moved off HEAD's history", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", "refs/remotes/upstream/tip", string(fx.sideCommit(t)))
			}}},
		},
		{
			name: "detached bookmark symlink target moved",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.detachAhead(t)
				fx.symlinkRef(t, "refs/heads/alias", "refs/remotes/side/tip", fx.root)
				fx.anchored(t, "trunk work", "main")
			},
			writes: []matrixWrite{{name: "symlink target outside refs/heads moved to HEAD", want: tierRevalidate, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", "refs/remotes/side/tip", string(fx.theirs))
			}}},
		},
		{
			name: "detached bookmark read through a filesystem symlink",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.detachAhead(t)
				fx.run(t, "update-ref", "refs/remotes/side/tip", string(fx.root))
				fx.anchored(t, "trunk work", "main")
			},
			writes: []matrixWrite{
				{name: "refs/heads/alias linked to ../remotes/side/tip", want: tierRevalidate, do: func(t *testing.T, fx *matrixFixture) {
					if err := os.Symlink(filepath.Join("..", "remotes", "side", "tip"), filepath.Join(fx.commonDir(t), "refs", "heads", "alias")); err != nil {
						t.Fatal(err)
					}
				}},
				{name: "side/tip moved to HEAD", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
					fx.run(t, "update-ref", "refs/remotes/side/tip", string(fx.theirs))
				}},
			},
			settled: tierRevalidate,
		},
		{
			name: "origin/HEAD made ambiguous by a tag",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.crossAuthor(t)
				fx.run(t, "update-ref", "refs/remotes/origin/main", string(fx.theirs))
				fx.run(t, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			},
			writes: []matrixWrite{{name: "refs/tags/origin/main created", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", "refs/tags/origin/main", string(fx.root))
			}}},
		},
		{
			name: "symlink HEAD switches branch",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "config", "core.preferSymlinkRefs", "true")
				fx.run(t, "branch", "other")
				fx.run(t, "checkout", "-q", "other")
				fx.run(t, "checkout", "-q", "main")
				if target, err := os.Readlink(filepath.Join(fx.commonDir(t), "HEAD")); err != nil || target != "refs/heads/main" {
					t.Fatalf("HEAD is not a symlink to refs/heads/main: %q, %v", target, err)
				}
				fx.anchored(t, "other work", "other")
			},
			writes: []matrixWrite{{name: "checkout other relinks HEAD", do: gitWrite("checkout", "-q", "other"), want: tierRebuild, changes: true}},
		},
		{
			name:   "author email changed in local config",
			setup:  func(t *testing.T, fx *matrixFixture) { fx.crossAuthor(t) },
			writes: []matrixWrite{{name: "git config user.email", do: gitWrite("config", "user.email", relevantOther), want: tierRebuild, changes: true}},
		},
		{
			name:  "include.path added, then its target created, then edited",
			setup: func(t *testing.T, fx *matrixFixture) { fx.crossAuthor(t) },
			writes: []matrixWrite{
				{name: "include.path names a missing file", do: gitWrite("config", "include.path", "extra.config"), want: tierRebuild},
				{name: "include target created", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
					fx.run(t, "config", "-f", filepath.Join(fx.commonDir(t), "extra.config"), "user.email", relevantOther)
				}},
				{name: "include target edited", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
					fx.run(t, "config", "-f", filepath.Join(fx.commonDir(t), "extra.config"), "user.email", "third@example.com")
				}},
			},
		},
		{
			name: "includeIf onbranch flips",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.crossAuthor(t)
				fx.run(t, "branch", "flip")
				fx.run(t, "config", "includeIf.onbranch:flip.path", "extra.config")
				fx.run(t, "config", "-f", filepath.Join(fx.commonDir(t), "extra.config"), "user.email", relevantOther)
			},
			writes: []matrixWrite{{name: "checkout the branch that activates the include", do: gitWrite("checkout", "-q", "flip"), want: tierRebuild, changes: true}},
		},
		{
			name: "global config file edited",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.crossAuthor(t)
				global := filepath.Join(t.TempDir(), "gitconfig")
				if err := os.WriteFile(global, []byte("[user]\n\temail = "+relevantMe+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GIT_CONFIG_GLOBAL", global)
				fx.run(t, "config", "--unset", "user.email")
			},
			writes: []matrixWrite{{name: "git config --global user.email", do: gitWrite("config", "--global", "user.email", relevantOther), want: tierRebuild, changes: true}},
		},
		{
			name: "GIT_CONFIG set over an included identity",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.crossAuthor(t)
				fx.includeExtra(t, relevantMe)
				t.Setenv("CC_NOTES_NOTE_STALE_AFTER", "2160h")
			},
			writes: []matrixWrite{
				{name: "GIT_CONFIG names a decoy file", want: tierRebuild, do: func(t *testing.T, _ *matrixFixture) {
					decoy := filepath.Join(t.TempDir(), "decoy.config")
					if err := os.WriteFile(decoy, []byte("[user]\n\temail = decoy@example.com\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("GIT_CONFIG", decoy)
				}},
				{name: "include target edited under GIT_CONFIG", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
					fx.run(t, "config", "-f", filepath.Join(fx.commonDir(t), "extra.config"), "user.email", relevantOther)
				}},
			},
		},
		{
			name: "staleness threshold changed in the environment",
			writes: []matrixWrite{{name: "CC_NOTES_NOTE_STALE_AFTER=1ns", want: tierRebuild, changes: true, do: func(t *testing.T, _ *matrixFixture) {
				t.Setenv("CC_NOTES_NOTE_STALE_AFTER", "1ns")
			}}},
		},
		{
			name: "git on PATH swapped",
			writes: []matrixWrite{{name: "a second git wrapper shadows the first", want: tierRebuild, do: func(t *testing.T, fx *matrixFixture) {
				fx.counter.prepend(t, countingGitScript)
			}}},
		},
		{
			name: "commit replaced",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.crossAuthor(t)
			},
			writes: []matrixWrite{{name: "git replace swaps the teammate commit for mine", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				mine := fx.run(t, "commit-tree", string(fx.theirs)+"^{tree}", "-p", string(fx.root), "-m", "mine")
				fx.run(t, "replace", string(fx.theirs), mine)
			}}},
		},
		{
			name:  "graft file written",
			setup: func(t *testing.T, fx *matrixFixture) { fx.crossAuthor(t) },
			writes: []matrixWrite{{name: "info/grafts cuts HEAD from its parent", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				if err := os.WriteFile(filepath.Join(fx.commonDir(t), "info", "grafts"), []byte(string(fx.theirs)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}}},
		},
		{
			name: "GIT_SHALLOW_FILE written",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.crossAuthor(t)
				t.Setenv("GIT_SHALLOW_FILE", filepath.Join(t.TempDir(), "shallow"))
			},
			writes: []matrixWrite{{name: "the overriding shallow file grafts HEAD", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				if err := os.WriteFile(os.Getenv("GIT_SHALLOW_FILE"), []byte(string(fx.theirs)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}}},
		},
		{
			name:  "repository made shallow",
			setup: func(t *testing.T, fx *matrixFixture) { fx.crossAuthor(t) },
			writes: []matrixWrite{{name: "shallow boundary at HEAD", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				gittest.Shallow(t, fx.repo, string(fx.theirs))
			}}},
		},
		{
			name: "tag-named commit anchor moved",
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "tag", "v1")
				storeCreate(t, fx.repo, model.CreateNote{Nonce: model.NewNonce(), Title: "tagged", Anchors: []model.Anchor{
					{Kind: model.AnchorCommit, Value: "v1"},
					{Kind: model.AnchorPath, Value: matrixTarget},
				}})
			},
			writes: []matrixWrite{{name: "v1 moved off HEAD's history", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				fx.run(t, "update-ref", "refs/tags/v1", string(fx.sideCommit(t)))
			}}},
		},
		{
			name:    "worktree drift under a configured filter driver",
			filter:  notes.RelevantFilter{Attached: true, Worktree: true},
			setup:   func(t *testing.T, fx *matrixFixture) { commitFile(t, fx.repo, ".gitattributes", "*.go filter=scrub\n") },
			writes:  []matrixWrite{{name: "filter.scrub.clean configured", do: gitWrite("config", "filter.scrub.clean", "cat"), want: tierRebuild}},
			settled: tierRebuild,
		},
		{
			name:   "per-worktree base ref in a linked worktree",
			filter: notes.RelevantFilter{Base: "worktree/x"},
			setup: func(t *testing.T, fx *matrixFixture) {
				fx.crossAuthor(t)
				fx.run(t, "checkout", "-q", "main")
				fx.dir = filepath.Join(t.TempDir(), "linked")
				fx.run(t, "worktree", "add", "-q", fx.dir, "work")
				gittest.Git(t, fx.dir, "update-ref", "refs/worktree/x", string(fx.root))
				c, err := notes.Open(fx.dir)
				if err != nil {
					t.Fatalf("Open linked worktree: %v", err)
				}
				fx.c = c
			},
			writes: []matrixWrite{{name: "refs/worktree/x moved to HEAD", want: tierRebuild, changes: true, do: func(t *testing.T, fx *matrixFixture) {
				gittest.Git(t, fx.dir, "update-ref", "refs/worktree/x", string(fx.theirs))
			}}},
		},
	}
}

func TestRelevantCachedWarmHitIsConstantWork(t *testing.T) {
	layouts := []struct {
		name   string
		layout warmLayout
		under  []string
	}{
		{name: "flat", layout: cyclingLayout([]string{"alpha", "beta", "gamma"}, nil), under: []string{"main"}},
		{name: "nested", layout: cyclingLayout([]string{"team-a/alpha", "team-b/beta", "team-c/gamma"}, nil), under: []string{"main", "team-a", "team-b", "team-c"}},
		{name: "diverse", layout: diverseLayout(nil), under: []string{"main"}},
	}
	sizes := []struct{ n, relevant int }{{8, 8}, {512, 8}, {512, 512}}
	for _, layout := range layouts {
		t.Run(layout.name, func(t *testing.T) {
			probes := make([]notes.RelevantCacheProbe, len(sizes))
			for i, size := range sizes {
				t.Run(fmt.Sprintf("N=%04d/relevant=%04d", size.n, size.relevant), func(t *testing.T) {
					t.Cleanup(notes.SetRelevantRacyWindow(0))
					dir := newRepo(t)
					root := commitFile(t, dir, matrixTarget, "v1\n")
					anchored := layout.layout(t, dir, root, size.n, size.relevant)
					c, err := notes.Open(dir)
					if err != nil {
						t.Fatalf("Open: %v", err)
					}
					p := &tierProbe{t: t, c: c, dir: dir, git: installCountingGit(t, countingGitScript)}
					p.expect("cold", tierRebuild)
					p.promoteThen("warm hit", tierHit)
					probe := p.entry("warm hit")
					if probe.Racy || probe.Revalidate {
						t.Fatalf("warm entry racy=%t revalidate=%t, want neither", probe.Racy, probe.Revalidate)
					}
					var headsDir int
					var under []string
					for _, stamp := range probe.Stamps {
						switch _, rest, found := strings.Cut(filepath.ToSlash(stamp), "/refs/heads"); {
						case !found:
						case rest == "":
							headsDir++
						case strings.HasPrefix(rest, "/"):
							under = append(under, rest[1:])
						}
					}
					slices.Sort(under)
					if headsDir != 1 || !slices.Equal(under, layout.under) {
						t.Fatalf("refs/heads stamps: directory %d times, below it %q; want the directory once and %q\nall stamps %q", headsDir, under, layout.under, probe.Stamps)
					}
					var served []json.RawMessage
					if err := json.Unmarshal(p.last, &served); err != nil || len(served) != size.relevant {
						t.Fatalf("warm hit served %d entries (%v), want the %d relevant ones", len(served), err, size.relevant)
					}
					if probe.DepBranches != len(anchored) || probe.DepCommits != 0 {
						t.Fatalf("deps = %d branches, %d commits; want the %d distinct branch anchors and no commits (header %d bytes)", probe.DepBranches, probe.DepCommits, len(anchored), probe.HeaderBytes)
					}
					probes[i] = probe
				})
			}
			for i, size := range sizes {
				if got, want := probes[i], probes[0]; len(got.Stamps) != len(want.Stamps) || got.Files != want.Files {
					t.Fatalf("warm-hit work varies with entity count: %d stamps, %d files, header %d bytes at N=%d/relevant=%d; %d stamps, %d files, header %d bytes at N=%d/relevant=%d", len(got.Stamps), got.Files, got.HeaderBytes, size.n, size.relevant, len(want.Stamps), want.Files, want.HeaderBytes, sizes[0].n, sizes[0].relevant)
				}
			}
		})
	}
}

func TestRelevantCachedWarmHitNeverDecodesDeps(t *testing.T) {
	const garbage, allocBound = 8 << 20, 1 << 20
	t.Cleanup(notes.SetRelevantRacyWindow(0))
	fx := newMatrixFixture(t)
	fx.anchored(t, "on main", "main")
	p := &tierProbe{t: t, c: fx.c, dir: fx.dir, git: installCountingGit(t, countingGitScript)}
	p.expect("cold", tierRebuild)
	p.promoteThen("warm hit", tierHit)
	if probe := p.entry("warm hit"); probe.DepBranches != 1 {
		t.Fatalf("settled entry carries %d branch deps (%d bytes), want the one anchored branch", probe.DepBranches, probe.DepsBytes)
	}
	served := p.last

	if err := notes.CorruptRelevantCacheDeps(fx.c, matrixTarget, p.filter, "json", garbage); err != nil {
		t.Fatalf("CorruptRelevantCacheDeps: %v", err)
	}
	if _, _, err := notes.RelevantCacheProbeOf(fx.c, matrixTarget, p.filter, "json"); err == nil {
		t.Fatal("the deps section still decodes after being overwritten")
	}
	var tier cacheTier
	allocated := totalAllocated(func() { tier = p.call("warm hit over garbage deps") })
	if tier != tierHit {
		t.Fatalf("warm hit over garbage deps: served by %v, want %v", tier, tierHit)
	}
	if !bytes.Equal(p.last, served) {
		t.Fatalf("hit over garbage deps served\n%s\nwant the settled answer\n%s", p.last, served)
	}
	if allocated >= allocBound {
		t.Fatalf("hit over %d bytes of garbage deps allocated %d bytes, want under %d: the hit read past its output", garbage, allocated, allocBound)
	}

	fx.run(t, "update-ref", "refs/heads/unrelated", string(fx.root))
	p.expect("moved stamp over garbage deps", tierRebuild)
	if probe := p.entry("rebuilt"); probe.DepBranches != 1 {
		t.Fatalf("rebuilt entry carries %d branch deps, want the one anchored branch", probe.DepBranches)
	}
}

func TestRelevantCachedRacyWindowSteadyState(t *testing.T) {
	fx := newMatrixFixture(t)
	fx.counter = installCountingGit(t, countingGitScript)
	p := &tierProbe{t: t, c: fx.c, dir: fx.dir, git: fx.counter}
	p.call("cold")

	const window = 6 * time.Second
	t.Cleanup(notes.SetRelevantRacyWindow(window))
	if _, _, err := fx.second(t).CreateNote(t.Context(), notes.NoteSpec{Title: "fresh write", Anchors: notes.AnchorSpec{Paths: []string{matrixTarget}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	written := stampTime(t, filepath.Join(fx.repo, ".git", "refs", "cc-notes", "notes"))
	inWindow := func(step string, want cacheTier) {
		t.Helper()
		p.expect(step, want)
		if !p.entry(step).Racy {
			t.Fatalf("%s: entry captured inside the racy window is not marked racy", step)
		}
	}
	inWindow("right after an entity write", tierRebuild)
	inWindow("still inside the racy window", tierRevalidate)

	time.Sleep(time.Until(written.Add(window + 500*time.Millisecond)))
	p.expect("after quiescence", tierRevalidate)
	if p.entry("after quiescence").Racy {
		t.Fatal("a revalidation after quiescence left the entry racy")
	}
	p.expect("promoted", tierHit)
}

func TestRelevantCachedColdBuildRaceNeverPins(t *testing.T) {
	raceNeverPins(t, "done", true)
}

func TestRelevantCachedEntityABADuringScoringNeverPins(t *testing.T) {
	raceNeverPins(t, "scoring", false)
}

func raceNeverPins(t *testing.T, next string, endsRaced bool) {
	t.Cleanup(notes.SetRelevantRacyWindow(0))
	fx := newMatrixFixture(t)
	fx.crossAuthor(t)
	ref := fx.noteRef()
	before := model.SHA(fx.run(t, "rev-parse", ref))
	s, err := store.Open(fx.repo)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	raced, err := s.PrepareAppendAt(t.Context(), ref, before, []model.Op{model.SetTitle{Title: "handler, raced"}})
	if err != nil {
		t.Fatalf("PrepareAppendAt: %v", err)
	}
	state := filepath.Join(t.TempDir(), "race")
	if err := os.WriteFile(state, []byte("idle"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CC_NOTES_RACE_STATE", state)
	t.Setenv("CC_NOTES_RACE_DIR", fx.repo)
	t.Setenv("CC_NOTES_RACE_REF", ref)
	t.Setenv("CC_NOTES_RACE_A", string(before))
	t.Setenv("CC_NOTES_RACE_B", string(raced.New))
	t.Setenv("CC_NOTES_RACE_NEXT", next)
	fx.counter = installCountingGit(t, racingGitScript)
	p := &tierProbe{t: t, c: fx.c, dir: fx.dir, git: fx.counter}

	if err := os.WriteFile(state, []byte("listing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := p.call("cold build raced by an entity write"); got != tierRebuild {
		t.Fatalf("racing cold build served by %v, want %v", got, tierRebuild)
	}
	if strings.Contains(string(p.last), "handler, raced") {
		t.Fatalf("racing build folded the live tip instead of the enumeration it captured: %s", p.last)
	}
	if got, err := os.ReadFile(state); err != nil || string(got) != "done" {
		t.Fatalf("race state = %q, %v; want done (the shim never saw both trigger points)", got, err)
	}
	if _, ok, err := notes.RelevantCacheProbeOf(fx.c, matrixTarget, p.filter, "json"); err != nil || ok {
		t.Fatalf("racing build persisted an entry: ok=%t err=%v", ok, err)
	}
	p.expect("first call after the race", tierRebuild)
	if raced := strings.Contains(string(p.last), "handler, raced"); raced != endsRaced {
		t.Fatalf("answer after the race carries the raced title = %t, want %t: %s", raced, endsRaced, p.last)
	}
	p.promoteThen("settled after the race", tierHit)
}

func TestRelevantCachedNeverPinsARootRefRacedDuringCapture(t *testing.T) {
	t.Cleanup(notes.SetRelevantRacyWindow(0))
	fx := newMatrixFixture(t)
	fx.armRootRace(t, "orig-head", "cat-file", string(fx.root), filepath.Join(fx.commonDir(t), "ORIG_HEAD"))
	filter := notes.RelevantFilter{Base: "ORIG_HEAD"}
	render := func(entries []notes.RelevantEntry) ([]byte, error) { return json.Marshal(entries) }
	if _, err := fx.c.RelevantCached(t.Context(), matrixTarget, filter, "json", render); err != nil {
		t.Fatalf("capture raced by ORIG_HEAD appearing: %v", err)
	}
	fx.rootRaceFired(t)
	if _, ok, err := notes.RelevantCacheProbeOf(fx.c, matrixTarget, filter, "json"); err != nil || ok {
		t.Fatalf("a capture whose base appeared and vanished persisted an entry: ok=%t err=%v", ok, err)
	}
	_, cachedErr := fx.c.RelevantCached(t.Context(), matrixTarget, filter, "json", render)
	_, freshErr := fx.c.Relevant(t.Context(), matrixTarget, filter)
	if cachedErr == nil || freshErr == nil || cachedErr.Error() != freshErr.Error() {
		t.Fatalf("after the race: cached %v, fresh %v; want the same missing-base error", cachedErr, freshErr)
	}
}

type includeLayout struct {
	file  string
	mkdir string
	link  string
	swap  string
}

type includeRace struct {
	path    string
	settled cacheTier
	layout  func(t *testing.T, common string) includeLayout
}

func TestRelevantCachedNeverPinsARootIncludeRacedDuringCapture(t *testing.T) {
	includeRaceNeverPins(t, includeRace{path: "extra.config", settled: tierHit, layout: func(_ *testing.T, common string) includeLayout {
		return includeLayout{file: filepath.Join(common, "extra.config")}
	}})
}

func TestRelevantCachedNeverPinsADanglingIncludeLinkRacedDuringCapture(t *testing.T) {
	includeRaceNeverPins(t, includeRace{path: "extra.config", settled: tierRevalidate, layout: func(t *testing.T, common string) includeLayout {
		external := filepath.Join(t.TempDir(), "external.config")
		symlink(t, external, filepath.Join(common, "extra.config"))
		return includeLayout{file: external}
	}})
}

func TestRelevantCachedNeverPinsAnIncludeLinkedThroughDotDotRacedDuringCapture(t *testing.T) {
	includeRaceNeverPins(t, includeRace{path: "extra.config", settled: tierRevalidate, layout: func(t *testing.T, common string) includeLayout {
		outside := t.TempDir()
		nested := filepath.Join(outside, "nested")
		mkdir(t, nested)
		symlink(t, nested, filepath.Join(common, "hop"))
		symlink(t, filepath.FromSlash("hop/../external.config"), filepath.Join(common, "extra.config"))
		return includeLayout{file: filepath.Join(outside, "external.config")}
	}})
}

func TestRelevantCachedNeverPinsAnIncludeBelowADanglingDirectoryLinkRacedDuringCapture(t *testing.T) {
	includeRaceNeverPins(t, includeRace{path: "inc/extra.config", settled: tierRevalidate, layout: func(t *testing.T, common string) includeLayout {
		missing := filepath.Join(t.TempDir(), "missing")
		symlink(t, missing, filepath.Join(common, "inc"))
		return includeLayout{file: filepath.Join(missing, "extra.config"), mkdir: missing}
	}})
}

func TestRelevantCachedNeverPinsAnIncludeClimbingThroughADirectoryLinkRacedDuringCapture(t *testing.T) {
	includeRaceNeverPins(t, includeRace{path: "hop/../external.config", settled: tierHit, layout: func(t *testing.T, common string) includeLayout {
		outside := t.TempDir()
		nested := filepath.Join(outside, "nested")
		mkdir(t, nested)
		symlink(t, nested, filepath.Join(common, "hop"))
		return includeLayout{file: filepath.Join(outside, "external.config")}
	}})
}

func TestRelevantCachedNeverPinsAnIncludeBehindALinkRetargetedDuringCapture(t *testing.T) {
	includeRaceNeverPins(t, includeRace{path: "inc/link/extra.config", settled: tierHit, layout: func(t *testing.T, common string) includeLayout {
		outside := t.TempDir()
		v1, v2, inc := filepath.Join(outside, "v1"), filepath.Join(outside, "v2"), filepath.Join(common, "inc")
		mkdir(t, v1)
		mkdir(t, v2)
		mkdir(t, inc)
		link := filepath.Join(inc, "link")
		symlink(t, v1, link)
		return includeLayout{file: filepath.Join(v2, "extra.config"), link: link, swap: v2}
	}})
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func readLink(t *testing.T, link string) string {
	t.Helper()
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func includeRaceNeverPins(t *testing.T, race includeRace) {
	t.Cleanup(notes.SetRelevantRacyWindow(0))
	fx := newMatrixFixture(t)
	fx.crossAuthor(t)
	fx.run(t, "config", "include.path", race.path)
	layout := race.layout(t, fx.commonDir(t))
	restore := ""
	if layout.link != "" {
		restore = readLink(t, layout.link)
	}
	t.Setenv("CC_NOTES_RACE_MKDIR", layout.mkdir)
	t.Setenv("CC_NOTES_RACE_LINK", layout.link)
	t.Setenv("CC_NOTES_RACE_LINK_SWAP", layout.swap)
	t.Setenv("CC_NOTES_RACE_LINK_RESTORE", restore)
	fx.armRootRace(t, "include", "GIT_AUTHOR_IDENT", relevantOther, layout.file)
	p := &tierProbe{t: t, c: fx.c, dir: fx.dir, git: fx.counter}
	if got := p.call("capture raced by the include appearing"); got != tierRebuild {
		t.Fatalf("raced capture served by %v, want %v", got, tierRebuild)
	}
	if bytes.Contains(p.last, []byte(`"cross-author"`)) {
		t.Fatalf("the raced identity still scored cross-author; the include never reached the identity read: %s", p.last)
	}
	fx.rootRaceFired(t)
	if layout.link != "" {
		if got := readLink(t, layout.link); got != restore {
			t.Fatalf("%s -> %s after the race, want %s restored", layout.link, got, restore)
		}
	}
	if probe, ok, err := notes.RelevantCacheProbeOf(fx.c, matrixTarget, p.filter, "json"); err != nil || (ok && !probe.Revalidate) {
		t.Fatalf("a capture whose include appeared and vanished persisted a settled entry: ok=%t err=%v %+v", ok, err, probe)
	}
	p.expect("first call after the race", tierRebuild)
	if !bytes.Contains(p.last, []byte(`"cross-author"`)) {
		t.Fatalf("answer after the race lost the cross-author signal: %s", p.last)
	}
	p.promoteThen("settled after the race", race.settled)
	if probe := p.entry("settled after the race"); probe.Revalidate != (race.settled == tierRevalidate) {
		t.Fatalf("settled entry revalidate = %t, want %t: %+v", probe.Revalidate, race.settled == tierRevalidate, probe)
	}
}

func TestRelevantCachedSeesAnIncludeCreatedWhereADirectoryLinkClimbsTo(t *testing.T) {
	includeServesWarmThenSees(t, "hop/../external.config", "include created where the link climbs to", func(t *testing.T, common string) string {
		outside := t.TempDir()
		nested := filepath.Join(outside, "nested")
		mkdir(t, nested)
		symlink(t, nested, filepath.Join(common, "hop"))
		return filepath.Join(outside, "external.config")
	})
}

func TestRelevantCachedServesAnIncludeAboveTheGitDirWarm(t *testing.T) {
	includeServesWarmThenSees(t, "../shared.gitconfig", "include above the git dir edited", func(t *testing.T, common string) string {
		shared := filepath.Join(filepath.Dir(common), "shared.gitconfig")
		writeConfig(t, shared, "Shared")
		return shared
	})
}

func TestRelevantCachedServesAnIncludeBehindADirectoryLinkWarm(t *testing.T) {
	includeServesWarmThenSees(t, "inc/extra.config", "include edited behind the link", func(t *testing.T, common string) string {
		target := t.TempDir()
		symlink(t, target, filepath.Join(common, "inc"))
		extra := filepath.Join(target, "extra.config")
		writeConfig(t, extra, "Linked")
		return extra
	})
}

func writeConfig(t *testing.T, file, name string) {
	t.Helper()
	if err := os.WriteFile(file, []byte("[user]\n\tname = "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func includeServesWarmThenSees(t *testing.T, path, step string, layout func(t *testing.T, common string) string) {
	t.Cleanup(notes.SetRelevantRacyWindow(0))
	fx := newMatrixFixture(t)
	fx.crossAuthor(t)
	file := layout(t, fx.commonDir(t))
	fx.run(t, "config", "include.path", path)
	p := &tierProbe{t: t, c: fx.c, dir: fx.dir, git: installCountingGit(t, countingGitScript)}
	p.expect("cold", tierRebuild)
	p.promoteThen("settled", tierHit)
	if probe := p.entry("settled"); probe.Revalidate {
		t.Fatalf("a quiet repository settled with revalidate set: %+v", probe)
	}
	if !bytes.Contains(p.last, []byte(`"cross-author"`)) {
		t.Fatalf("settled answer lacks cross-author before the include names the teammate: %s", p.last)
	}
	fx.run(t, "config", "--file", file, "user.email", relevantOther)
	p.expect(step, tierRebuild)
	if bytes.Contains(p.last, []byte(`"cross-author"`)) {
		t.Fatalf("%s: the identity the include now sets never reached the answer: %s", step, p.last)
	}
}

func TestRelevantCachedWatchesASharedSymrefTailFromItsShallowestArrival(t *testing.T) {
	t.Cleanup(notes.SetRelevantRacyWindow(0))
	fx := newMatrixFixture(t)
	fx.symlinkRef(t, "refs/heads/f", "refs/remotes/upstream/tip", fx.root)
	for _, hop := range [][2]string{{"e", "f"}, {"d", "e"}, {"c", "d"}, {"b", "c"}, {"a", "b"}} {
		fx.run(t, "symbolic-ref", "refs/heads/"+hop[0], "refs/heads/"+hop[1])
	}
	fx.anchored(t, "aliased work", "a", "f")
	p := &tierProbe{t: t, c: fx.c, dir: fx.dir, git: installCountingGit(t, countingGitScript)}
	p.expect("cold", tierRebuild)
	p.promoteThen("settled", tierRevalidate)
	probe := p.entry("settled")
	if !probe.Revalidate {
		t.Fatalf("a symref chain git refuses to resolve settled without revalidate: %+v", probe)
	}
	if !slices.ContainsFunc(probe.Stamps, func(s string) bool { return strings.HasSuffix(filepath.ToSlash(s), "/refs/remotes/upstream") }) {
		t.Fatalf("f, first reached at the depth limit through a, was never traversed from its own anchor: no stamp on refs/remotes/upstream\nall stamps %q", probe.Stamps)
	}
	before := p.last
	fx.run(t, "update-ref", "refs/remotes/upstream/tip", string(fx.sideCommit(t)))
	p.expect("upstream/tip moved off HEAD's history", tierRebuild)
	if bytes.Equal(before, p.last) {
		t.Fatalf("moving the shared tail left the answer unchanged: %s", p.last)
	}
}

func TestRelevantCachedDetachedSymlinkAliasesShareOneTargetDirectoryStamp(t *testing.T) {
	const tips = 20
	counts := make(map[int]int)
	for _, aliases := range []int{0, 1, tips} {
		t.Run(fmt.Sprintf("aliases=%02d", aliases), func(t *testing.T) {
			t.Cleanup(notes.SetRelevantRacyWindow(0))
			fx := newMatrixFixture(t)
			fx.detachAhead(t)
			for i := range tips {
				target := fmt.Sprintf("refs/remotes/side/tip%02d", i)
				if i < aliases {
					fx.symlinkRef(t, fmt.Sprintf("refs/heads/alias%02d", i), target, fx.root)
					continue
				}
				fx.run(t, "update-ref", target, string(fx.root))
			}
			fx.anchored(t, "trunk work", "main")
			p := &tierProbe{t: t, c: fx.c, dir: fx.dir, git: installCountingGit(t, countingGitScript)}
			p.expect("cold", tierRebuild)
			p.promoteThen("warm", tierHit)
			probe := p.entry("warm")
			var sideDir, sideLeaves int
			for _, stamp := range probe.Stamps {
				switch _, rest, found := strings.Cut(filepath.ToSlash(stamp), "/refs/remotes/side"); {
				case !found:
				case rest == "":
					sideDir++
				case strings.HasPrefix(rest, "/"):
					sideLeaves++
				}
			}
			if want := min(aliases, 1); sideDir != want || sideLeaves != 0 {
				t.Fatalf("refs/remotes/side stamps: directory %d times, %d leaves below it; want the directory %d times and no leaves\nall stamps %q", sideDir, sideLeaves, want, probe.Stamps)
			}
			counts[aliases] = len(probe.Stamps)
		})
	}
	if t.Failed() {
		return
	}
	if counts[tips] != counts[1] || counts[1] != counts[0]+1 {
		t.Fatalf("stamps at 0/1/%d aliases = %d/%d/%d; want one directory stamp for the first alias and none for the rest", tips, counts[0], counts[1], counts[tips])
	}
}

func TestRelevantCachedDetachedRootRefAliasesPersistNoLeafStamps(t *testing.T) {
	const aliases = 20
	counts := make(map[int]int)
	for _, n := range []int{0, aliases} {
		t.Run(fmt.Sprintf("aliases=%02d", n), func(t *testing.T) {
			t.Cleanup(notes.SetRelevantRacyWindow(0))
			fx := newMatrixFixture(t)
			fx.detachAhead(t)
			for i := range n {
				root := fmt.Sprintf("ROOTREF_%02d", i)
				fx.run(t, "update-ref", root, string(fx.root))
				fx.run(t, "symbolic-ref", fmt.Sprintf("refs/heads/alias%02d", i), root)
			}
			fx.anchored(t, "trunk work", "main")
			p := &tierProbe{t: t, c: fx.c, dir: fx.dir, git: installCountingGit(t, countingGitScript)}
			p.expect("cold", tierRebuild)
			want := tierHit
			if n > 0 {
				want = tierRevalidate
			}
			p.promoteThen("warm", want)
			probe := p.entry("warm")
			if probe.Revalidate != (n > 0) {
				t.Fatalf("revalidate = %t with %d root-ref aliases, want %t: %+v", probe.Revalidate, n, n > 0, probe)
			}
			if leaf := slices.IndexFunc(probe.Stamps, func(s string) bool { return strings.HasPrefix(filepath.Base(s), "ROOTREF_") }); leaf >= 0 {
				t.Fatalf("a root ref reached only through an iterated alias was leaf-stamped: %s\nall stamps %q", probe.Stamps[leaf], probe.Stamps)
			}
			counts[n] = len(probe.Stamps)
		})
	}
	if t.Failed() {
		return
	}
	if counts[aliases] != counts[0] {
		t.Fatalf("stamps at 0/%d root-ref aliases = %d/%d; want the same count", aliases, counts[0], counts[aliases])
	}
}

func totalAllocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func stampTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ctimeNanos, _ := gitobj.StatIdentity(info)
	if ctime := time.Unix(0, ctimeNanos); ctime.After(info.ModTime()) {
		return ctime
	}
	return info.ModTime()
}
