package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// sharedFixture is a full source repository and a depth-1 clone of it with
// two linked worktrees: the shape a thin ccx store takes. Nothing is bound
// until a test says so.
type sharedFixture struct {
	source, sourceCommon string
	thin, thinCommon     string
	worktreeA, worktreeB string
}

func newSharedFixture(t *testing.T) *sharedFixture {
	t.Helper()
	source := initSourceRepo(t)
	thin := gittest.ShallowClone(t, source, 1)
	_, thinCommon := gittest.Dirs(t, thin)
	return &sharedFixture{
		source:       source,
		sourceCommon: canonical(t, filepath.Join(source, ".git")),
		thin:         thin,
		thinCommon:   thinCommon,
		worktreeA:    gittest.AddWorktree(t, thin, "wt-a"),
		worktreeB:    gittest.AddWorktree(t, thin, "wt-b"),
	}
}

func (f *sharedFixture) bind(t *testing.T) BindResult {
	t.Helper()
	result, err := Bind(t.Context(), f.thin, f.source)
	if err != nil {
		t.Fatalf("Bind(thin, source): %v", err)
	}
	return result
}

func (f *sharedFixture) config() string { return filepath.Join(f.thinCommon, "config") }

// assertNoRecords proves the thin repository gained neither cc-notes refs nor a
// fold cache: a failed or refused binding never falls back to a local corpus.
func (f *sharedFixture) assertNoRecords(t *testing.T) {
	t.Helper()
	if out := gittest.Git(t, f.thin, "for-each-ref", "refs/cc-notes/"); out != "" {
		t.Fatalf("thin repository holds cc-notes refs:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.thinCommon, foldCacheSubdir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("thin repository holds a fold cache dir: stat %v", err)
	}
}

// initSourceRepo is a scrubbed repository with one commit, enough to clone.
func initSourceRepo(t *testing.T) string {
	t.Helper()
	dir := gittest.InitRepo(t)
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	return dir
}

func canonical(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", path, err)
	}
	return resolved
}

// bindingFor is the binding a correct Bind would publish for commonDir.
func bindingFor(t *testing.T, commonDir string) Binding {
	t.Helper()
	dir := canonical(t, commonDir)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	device, inode := gitobj.FileID(info)
	return Binding{CommonDir: dir, Device: device, Inode: inode}
}

func setBindingValue(t *testing.T, configPath, value string) {
	t.Helper()
	gittest.Git(t, filepath.Dir(configPath), "config", "--file", configPath, bindingKey, value)
}

// replaceRepo moves the repository at dir aside and initializes a new one in
// its place, so the path answers with a different device/inode identity.
func replaceRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.Rename(dir, dir+".replaced"); err != nil {
		t.Fatalf("move %s aside: %v", dir, err)
	}
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Git(t, dir, "config", "user.name", testName)
	gittest.Git(t, dir, "config", "user.email", testEmail)
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "replacement")
}

func assertBindingError(t *testing.T, err, sentinel error, wantConfig, wantSource string) *BindingError {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil, want %v", sentinel)
	}
	var be *BindingError
	if !errors.As(err, &be) {
		t.Fatalf("got %T %v, want *BindingError", err, err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want %v", err, sentinel)
	}
	if canonical(t, be.Config) != canonical(t, wantConfig) {
		t.Fatalf("BindingError.Config = %q, want %q", be.Config, wantConfig)
	}
	if be.Source != wantSource {
		t.Fatalf("BindingError.Source = %q, want %q", be.Source, wantSource)
	}
	msg := err.Error()
	if !strings.Contains(msg, be.Config) || !strings.Contains(msg, bindingKey) || !strings.Contains(msg, sentinel.Error()) {
		t.Fatalf("message %q lacks the config path, the key, or the sentinel", msg)
	}
	if wantSource != "" && !strings.Contains(msg, wantSource) {
		t.Fatalf("message %q lacks the source %q", msg, wantSource)
	}
	return be
}

func TestBindingCodec(t *testing.T) {
	b := Binding{CommonDir: "/repos/source/.git", Device: 16777232, Inode: 41}
	const want = `{"version":1,"commonDir":"/repos/source/.git","device":16777232,"inode":41}`
	if got := b.String(); got != want {
		t.Fatalf("String() = %s, want %s", got, want)
	}
	for _, raw := range []string{want, " {\"version\": 1,\n\t\"commonDir\": \"/repos/source/.git\", \"device\": 16777232, \"inode\": 41 }\n"} {
		parsed, err := parseBinding(raw)
		if err != nil {
			t.Fatalf("parseBinding(%q): %v", raw, err)
		}
		if parsed != b {
			t.Fatalf("parseBinding(%q) = %+v, want %+v", raw, parsed, b)
		}
	}
	rejects := []struct{ name, raw string }{
		{"unknown field", `{"version":1,"commonDir":"/r/.git","device":1,"inode":2,"extra":true}`},
		{"version 2", `{"version":2,"commonDir":"/r/.git","device":1,"inode":2}`},
		{"version missing", `{"commonDir":"/r/.git","device":1,"inode":2}`},
		{"relative path", `{"version":1,"commonDir":"r/.git","device":1,"inode":2}`},
		{"unclean path", `{"version":1,"commonDir":"/r//.git","device":1,"inode":2}`},
		{"dot segment", `{"version":1,"commonDir":"/r/./.git","device":1,"inode":2}`},
		{"trailing slash", `{"version":1,"commonDir":"/r/.git/","device":1,"inode":2}`},
		{"zero inode", `{"version":1,"commonDir":"/r/.git","device":1,"inode":0}`},
		{"trailing object", `{"version":1,"commonDir":"/r/.git","device":1,"inode":2}{}`},
		{"trailing text", `{"version":1,"commonDir":"/r/.git","device":1,"inode":2} x`},
		{"bad json", `{nope`},
		{"not an object", `"/r/.git"`},
		{"null", `null`},
		{"empty", ``},
		{"wrong type", `{"version":"1","commonDir":"/r/.git","device":1,"inode":2}`},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseBinding(tc.raw); !errors.Is(err, ErrBindingMalformed) {
				t.Fatalf("parseBinding(%q) = %v, want ErrBindingMalformed", tc.raw, err)
			}
		})
	}
}

func TestOpenContextUnboundUnchanged(t *testing.T) {
	s := initStore(t)
	if s.ContextRepo != s.Repo {
		t.Fatal("unbound ContextRepo is not the same handle as Repo")
	}
	if s.RecordsGit != s.Git {
		t.Fatalf("unbound RecordsGit = %+v, want Git %+v", s.RecordsGit, s.Git)
	}
	if s.RecordsCommonDir() != s.CommonDir() {
		t.Fatalf("unbound RecordsCommonDir = %q, want CommonDir %q", s.RecordsCommonDir(), s.CommonDir())
	}
	if want := filepath.Join(s.CommonDir(), foldCacheSubdir); s.cache.dir != want {
		t.Fatalf("fold cache dir = %q, want %q", s.cache.dir, want)
	}
	if want := filepath.Join(s.CommonDir(), relevantCacheSubdir); s.relevant.dir != want {
		t.Fatalf("relevance cache dir = %q, want %q", s.relevant.dir, want)
	}
	if b, bound := s.Binding(); bound {
		t.Fatalf("unbound store reports binding %+v", b)
	}
	if want := filepath.Join(s.CommonDir(), "config"); s.storage.config != want {
		t.Fatalf("binding record watches %q, want %q", s.storage.config, want)
	}
	if err := s.CheckRecords(); err != nil {
		t.Fatalf("CheckRecords: %v", err)
	}
	p := s.Pinned(nil)
	if p.storage != s.storage || p.RecordsGit != s.RecordsGit || p.ContextRepo != s.ContextRepo || p.recordsCommonDir != s.recordsCommonDir {
		t.Fatal("Pinned view does not share the roles and binding record")
	}
}

func TestOpenContextBound(t *testing.T) {
	f := newSharedFixture(t)
	f.bind(t)
	ctx := t.Context()
	ref := refs.For(model.KindNote, model.EntityID(strings.Repeat("ab", 20)))
	gittest.Git(t, f.source, "update-ref", ref, "HEAD")
	sourceHead := model.SHA(gittest.Git(t, f.source, "rev-parse", "HEAD"))

	for _, dir := range []string{f.thin, f.worktreeA, f.worktreeB} {
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open(%s): %v", dir, err)
		}
		if s.Repo == s.ContextRepo {
			t.Fatalf("Open(%s): records and context share one object database", dir)
		}
		if s.RecordsGit != gitcmd.Backend(f.sourceCommon, filepath.Dir(f.sourceCommon)) {
			t.Fatalf("Open(%s): RecordsGit = %+v, want a Backend handle on %q", dir, s.RecordsGit, f.sourceCommon)
		}
		if s.Git != (gitcmd.Git{Dir: dir}) {
			t.Fatalf("Open(%s): Git = %+v, want a checkout handle on %q", dir, s.Git, dir)
		}
		if s.RecordsCommonDir() != f.sourceCommon {
			t.Fatalf("Open(%s): RecordsCommonDir = %q, want %q", dir, s.RecordsCommonDir(), f.sourceCommon)
		}
		if canonical(t, s.CommonDir()) != canonical(t, f.thinCommon) {
			t.Fatalf("Open(%s): CommonDir = %q, want the thin %q", dir, s.CommonDir(), f.thinCommon)
		}
		if want := filepath.Join(f.sourceCommon, foldCacheSubdir); s.cache.dir != want {
			t.Fatalf("Open(%s): fold cache dir = %q, want %q", dir, s.cache.dir, want)
		}
		if want := filepath.Join(s.CommonDir(), relevantCacheSubdir); s.relevant.dir != want {
			t.Fatalf("Open(%s): relevance cache dir = %q, want %q", dir, s.relevant.dir, want)
		}
		if b, bound := s.Binding(); !bound || b != bindingFor(t, f.sourceCommon) {
			t.Fatalf("Open(%s): Binding() = %+v, %v; want %+v", dir, b, bound, bindingFor(t, f.sourceCommon))
		}
		if err := s.CheckRecords(); err != nil {
			t.Fatalf("Open(%s): CheckRecords: %v", dir, err)
		}
		p := s.Pinned(nil)
		if p.Repo != s.Repo || p.ContextRepo != s.ContextRepo || p.RecordsGit != s.RecordsGit || p.recordsCommonDir != s.recordsCommonDir || p.storage != s.storage || p.cache != s.cache {
			t.Fatalf("Open(%s): Pinned view drops a role", dir)
		}
		tip, err := s.Repo.Tip(ctx, ref)
		if err != nil {
			t.Fatalf("Open(%s): records Tip: %v", dir, err)
		}
		if tip != sourceHead {
			t.Fatalf("Open(%s): records Tip = %s, want the source's %s", dir, tip, sourceHead)
		}
		if _, err := s.ContextRepo.Tip(ctx, ref); !errors.Is(err, gitobj.ErrRefNotFound) {
			t.Fatalf("Open(%s): context Tip = %v, want ErrRefNotFound", dir, err)
		}
		head, err := s.ContextRepo.Tip(ctx, "HEAD")
		if err != nil {
			t.Fatalf("Open(%s): context HEAD: %v", dir, err)
		}
		if want := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD")); head != want {
			t.Fatalf("Open(%s): context HEAD = %s, want %s", dir, head, want)
		}
	}
	s, err := Open(f.worktreeA)
	if err != nil {
		t.Fatalf("Open(worktree): %v", err)
	}
	if !strings.HasPrefix(canonical(t, s.GitDir()), filepath.Join(canonical(t, f.thinCommon), "worktrees")+string(filepath.Separator)) {
		t.Fatalf("worktree GitDir = %q, want one under %s/worktrees", s.GitDir(), f.thinCommon)
	}
	src, err := Open(f.source)
	if err != nil {
		t.Fatalf("Open(source): %v", err)
	}
	if src.Repo != src.ContextRepo || src.RecordsGit != src.Git {
		t.Fatal("the source itself must stay unbound")
	}
	f.assertNoRecords(t)
}

func TestOpenContextBindingFailures(t *testing.T) {
	rows := []struct {
		name     string
		mutate   func(t *testing.T, f *sharedFixture) (wantSource string)
		sentinel error
	}{
		{
			name: "backend removed",
			mutate: func(t *testing.T, f *sharedFixture) string {
				if err := os.RemoveAll(f.source); err != nil {
					t.Fatalf("remove source: %v", err)
				}
				return f.sourceCommon
			},
			sentinel: ErrBackendUnavailable,
		},
		{
			name: "backend replaced at the same path",
			mutate: func(t *testing.T, f *sharedFixture) string {
				replaceRepo(t, f.source)
				return f.sourceCommon
			},
			sentinel: ErrBackendReplaced,
		},
		{
			name: "corrupt value",
			mutate: func(t *testing.T, f *sharedFixture) string {
				setBindingValue(t, f.config(), `{nope`)
				return ""
			},
			sentinel: ErrBindingMalformed,
		},
		{
			name: "version 2",
			mutate: func(t *testing.T, f *sharedFixture) string {
				b := bindingFor(t, f.sourceCommon)
				setBindingValue(t, f.config(), strings.Replace(b.String(), `"version":1`, `"version":2`, 1))
				return ""
			},
			sentinel: ErrBindingMalformed,
		},
		{
			name: "two values",
			mutate: func(t *testing.T, f *sharedFixture) string {
				gittest.Git(t, f.thin, "config", "--file", f.config(), "--add", bindingKey, bindingFor(t, f.sourceCommon).String())
				return ""
			},
			sentinel: ErrBindingMalformed,
		},
		{
			name: "backend itself bound",
			mutate: func(t *testing.T, f *sharedFixture) string {
				if _, err := Bind(t.Context(), f.source, initSourceRepo(t)); err != nil {
					t.Fatalf("bind the source onward: %v", err)
				}
				return f.sourceCommon
			},
			sentinel: ErrBackendRedirects,
		},
		{
			name: "binding to the context itself",
			mutate: func(t *testing.T, f *sharedFixture) string {
				b := bindingFor(t, f.thinCommon)
				setBindingValue(t, f.config(), b.String())
				return b.CommonDir
			},
			sentinel: ErrBindingCycle,
		},
		{
			name: "linked-worktree gitdir as backend",
			mutate: func(t *testing.T, f *sharedFixture) string {
				gitDir, _ := gittest.Dirs(t, gittest.AddWorktree(t, f.source, "side"))
				b := bindingFor(t, gitDir)
				setBindingValue(t, f.config(), b.String())
				return b.CommonDir
			},
			sentinel: ErrBackendRedirects,
		},
		{
			name: "reftable backend",
			mutate: func(t *testing.T, f *sharedFixture) string {
				b := bindingFor(t, filepath.Join(initReftableRepo(t), ".git"))
				setBindingValue(t, f.config(), b.String())
				return b.CommonDir
			},
			sentinel: ErrBackendUnavailable,
		},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			f.bind(t)
			wantSource := tc.mutate(t, f)
			for _, dir := range []string{f.thin, f.worktreeA} {
				s, err := Open(dir)
				if s != nil {
					t.Fatalf("Open(%s) returned a store under a broken binding", dir)
				}
				_ = assertBindingError(t, err, tc.sentinel, f.config(), wantSource)
			}
			f.assertNoRecords(t)
		})
	}
}

// initReftableRepo returns a repository on the reftable ref backend, or skips
// when the local git cannot create one.
func initReftableRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	if out, err := exec.Command("git", "-C", dir, "init", "-q", "--ref-format=reftable", "-b", "main").CombinedOutput(); err != nil {
		t.Skipf("git init --ref-format=reftable unsupported: %v: %s", err, out)
	}
	gittest.Git(t, dir, "config", "user.name", testName)
	gittest.Git(t, dir, "config", "user.email", testEmail)
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	return dir
}

func TestCheckRecordsPreOpened(t *testing.T) {
	rows := []struct {
		name     string
		bound    bool
		mutate   func(t *testing.T, f *sharedFixture) (wantSource string)
		sentinel error
	}{
		{
			name:  "replace backend",
			bound: true,
			mutate: func(t *testing.T, f *sharedFixture) string {
				replaceRepo(t, f.source)
				return f.sourceCommon
			},
			sentinel: ErrBackendReplaced,
		},
		{
			name:  "remove backend",
			bound: true,
			mutate: func(t *testing.T, f *sharedFixture) string {
				if err := os.RemoveAll(f.source); err != nil {
					t.Fatalf("remove source: %v", err)
				}
				return f.sourceCommon
			},
			sentinel: ErrBackendUnavailable,
		},
		{
			name:  "rewrite binding to another valid backend",
			bound: true,
			mutate: func(t *testing.T, f *sharedFixture) string {
				setBindingValue(t, f.config(), bindingFor(t, filepath.Join(initSourceRepo(t), ".git")).String())
				return f.sourceCommon
			},
			sentinel: ErrBindingChanged,
		},
		{
			name:  "remove binding",
			bound: true,
			mutate: func(t *testing.T, f *sharedFixture) string {
				gittest.Git(t, f.thin, "config", "--file", f.config(), "--unset", bindingKey)
				return f.sourceCommon
			},
			sentinel: ErrBindingChanged,
		},
		{
			name:  "corrupt binding",
			bound: true,
			mutate: func(t *testing.T, f *sharedFixture) string {
				setBindingValue(t, f.config(), `{nope`)
				return f.sourceCommon
			},
			sentinel: ErrBindingMalformed,
		},
		{
			name:  "touch config without a binding change",
			bound: true,
			mutate: func(t *testing.T, f *sharedFixture) string {
				later := time.Now().Add(time.Hour)
				if err := os.Chtimes(f.config(), later, later); err != nil {
					t.Fatalf("touch config: %v", err)
				}
				return ""
			},
		},
		{
			name:  "rewrite config bytes without a binding change",
			bound: true,
			mutate: func(t *testing.T, f *sharedFixture) string {
				gittest.Git(t, f.thin, "config", "--file", f.config(), "cc-notes.unrelated", "1")
				return ""
			},
		},
		{
			name:  "unbound store sees a binding published after open",
			bound: false,
			mutate: func(t *testing.T, f *sharedFixture) string {
				f.bind(t)
				return ""
			},
			sentinel: ErrBindingChanged,
		},
		{
			name:  "unbound store tolerates unrelated config writes",
			bound: false,
			mutate: func(t *testing.T, f *sharedFixture) string {
				gittest.Git(t, f.thin, "config", "cc-notes.unrelated", "1")
				return ""
			},
		},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			if tc.bound {
				f.bind(t)
			}
			s, err := Open(f.worktreeA)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := s.CheckRecords(); err != nil {
				t.Fatalf("CheckRecords before mutation: %v", err)
			}
			wantSource := tc.mutate(t, f)
			err = s.CheckRecords()
			if tc.sentinel == nil {
				if err != nil {
					t.Fatalf("CheckRecords = %v, want nil", err)
				}
				if err := s.CheckRecords(); err != nil {
					t.Fatalf("second CheckRecords = %v, want nil", err)
				}
				return
			}
			_ = assertBindingError(t, err, tc.sentinel, f.config(), wantSource)
			if again := s.CheckRecords(); !errors.Is(again, tc.sentinel) {
				t.Fatalf("CheckRecords stopped failing on the second call: %v", again)
			}
			f.assertNoRecords(t)
		})
	}

	t.Run("a rewritten binding is served after reopen", func(t *testing.T) {
		f := newSharedFixture(t)
		f.bind(t)
		s, err := Open(f.thin)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		other := bindingFor(t, filepath.Join(initSourceRepo(t), ".git"))
		setBindingValue(t, f.config(), other.String())
		if err := s.CheckRecords(); !errors.Is(err, ErrBindingChanged) {
			t.Fatalf("CheckRecords = %v, want ErrBindingChanged", err)
		}
		reopened, err := Open(f.thin)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if reopened.RecordsCommonDir() != other.CommonDir {
			t.Fatalf("reopened RecordsCommonDir = %q, want %q", reopened.RecordsCommonDir(), other.CommonDir)
		}
	})

	t.Run("steady state spawns no git", func(t *testing.T) {
		f := newSharedFixture(t)
		f.bind(t)
		s, err := Open(f.worktreeB)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Setenv("PATH", t.TempDir())
		for range 3 {
			if err := s.CheckRecords(); err != nil {
				t.Fatalf("CheckRecords without git on PATH: %v", err)
			}
		}
		later := time.Now().Add(time.Hour)
		if err := os.Chtimes(f.config(), later, later); err != nil {
			t.Fatalf("touch config: %v", err)
		}
		if err := s.CheckRecords(); err != nil {
			t.Fatalf("CheckRecords re-read without git on PATH: %v", err)
		}
	})
}
