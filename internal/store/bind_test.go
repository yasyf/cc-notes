package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
)

// repoCensus is everything Bind must leave alone in a repository it only
// reads: its config bytes, its refs, and its object count.
type repoCensus struct {
	config, refs, objects string
}

func censusOf(t *testing.T, dir string) repoCensus {
	t.Helper()
	_, common := gittest.Dirs(t, dir)
	//nolint:gosec // G304: reads the test repository's own config.
	config, err := os.ReadFile(filepath.Join(common, "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return repoCensus{
		config:  string(config),
		refs:    gittest.Git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)"),
		objects: gittest.Git(t, dir, "count-objects", "-v"),
	}
}

func TestBind(t *testing.T) {
	rows := []struct {
		name string
		// arrange returns the --source argument and the common dir the binding
		// must name; "" when an error is expected.
		arrange     func(t *testing.T, f *sharedFixture) (source, wantCommon string)
		sentinel    error
		wantChanged bool
		check       func(t *testing.T, f *sharedFixture, before repoCensus)
	}{
		{
			name:        "fresh",
			arrange:     func(_ *testing.T, f *sharedFixture) (string, string) { return f.source, f.sourceCommon },
			wantChanged: true,
		},
		{
			name: "identical rebind is idempotent",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				f.bind(t)
				return f.source, f.sourceCommon
			},
			check: func(t *testing.T, f *sharedFixture, _ repoCensus) {
				before := censusOf(t, f.thin)
				if _, err := Bind(t.Context(), f.worktreeB, f.source); err != nil {
					t.Fatalf("rebind: %v", err)
				}
				if after := censusOf(t, f.thin); after != before {
					t.Fatalf("idempotent rebind rewrote the context:\nbefore %+v\nafter  %+v", before, after)
				}
			},
		},
		{
			name: "different source",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				f.bind(t)
				return initSourceRepo(t), ""
			},
			sentinel: ErrBindingConflict,
		},
		{
			name:     "source is the context",
			arrange:  func(_ *testing.T, f *sharedFixture) (string, string) { return f.thin, "" },
			sentinel: ErrBindingCycle,
		},
		{
			name:     "source is a worktree of the context",
			arrange:  func(_ *testing.T, f *sharedFixture) (string, string) { return f.worktreeB, "" },
			sentinel: ErrBindingCycle,
		},
		{
			name: "self-bound context rebinding to itself is a cycle, not idempotent",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				setBindingValue(t, f.config(), bindingFor(t, f.thinCommon).String())
				return f.thin, ""
			},
			sentinel: ErrBindingCycle,
		},
		{
			name: "bound source stores its backend directly",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				deep := initSourceRepo(t)
				if _, err := Bind(t.Context(), f.source, deep); err != nil {
					t.Fatalf("bind source to deep: %v", err)
				}
				return f.source, canonical(t, filepath.Join(deep, ".git"))
			},
			wantChanged: true,
		},
		{
			name: "bound source whose backend is the context",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				if _, err := Bind(t.Context(), f.source, f.thin); err != nil {
					t.Fatalf("bind source to thin: %v", err)
				}
				return f.source, ""
			},
			sentinel: ErrBindingCycle,
		},
		{
			name: "bound source whose backend is replaced",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				deep := initSourceRepo(t)
				if _, err := Bind(t.Context(), f.source, deep); err != nil {
					t.Fatalf("bind source to deep: %v", err)
				}
				replaceRepo(t, deep)
				return f.source, ""
			},
			sentinel: ErrBackendReplaced,
		},
		{
			name: "bound source whose backend is itself bound",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				deep := initSourceRepo(t)
				if _, err := Bind(t.Context(), f.source, deep); err != nil {
					t.Fatalf("bind source to deep: %v", err)
				}
				deeper := bindingFor(t, filepath.Join(initSourceRepo(t), ".git"))
				setBindingValue(t, filepath.Join(deep, ".git", "config"), deeper.String())
				return f.source, ""
			},
			sentinel: ErrBackendRedirects,
		},
		{
			name: "source carrying a malformed binding",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				setBindingValue(t, filepath.Join(f.source, ".git", "config"), `{nope`)
				return f.source, ""
			},
			sentinel: ErrBindingMalformed,
		},
		{
			name: "context holding records",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				gittest.Git(t, f.thin, "update-ref", "refs/cc-notes/notes/x", "HEAD")
				return f.source, ""
			},
			sentinel: ErrContextHasRecords,
			check: func(t *testing.T, f *sharedFixture, _ repoCensus) {
				_, err := Bind(t.Context(), f.thin, f.source)
				if !strings.Contains(err.Error(), "refs/cc-notes/notes/x") {
					t.Fatalf("refusal %q does not name the ref it found", err)
				}
			},
		},
		{
			name: "source given as a subdirectory",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				sub := filepath.Join(f.source, "deep", "er")
				if err := os.MkdirAll(sub, 0o750); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				return sub, f.sourceCommon
			},
			wantChanged: true,
		},
		{
			name: "source given as a linked worktree",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				return gittest.AddWorktree(t, f.source, "side"), f.sourceCommon
			},
			wantChanged: true,
		},
		{
			name: "source given through a symlink",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(f.source, alias); err != nil {
					t.Fatalf("symlink: %v", err)
				}
				return alias, f.sourceCommon
			},
			wantChanged: true,
		},
		{
			name: "malformed existing",
			arrange: func(t *testing.T, f *sharedFixture) (string, string) {
				setBindingValue(t, f.config(), `{nope`)
				return f.source, ""
			},
			sentinel: ErrBindingMalformed,
		},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			source, wantCommon := tc.arrange(t, f)
			sourceBefore := censusOf(t, f.source)
			thinBefore := censusOf(t, f.thin)

			result, err := Bind(t.Context(), f.thin, source)

			if sourceAfter := censusOf(t, f.source); sourceAfter != sourceBefore {
				t.Fatalf("Bind changed the source:\nbefore %+v\nafter  %+v", sourceBefore, sourceAfter)
			}
			if tc.sentinel != nil {
				if !errors.Is(err, tc.sentinel) {
					t.Fatalf("Bind = %v, want %v", err, tc.sentinel)
				}
				var be *BindingError
				if !errors.As(err, &be) {
					t.Fatalf("Bind = %T %v, want *BindingError", err, err)
				}
				if thinAfter := censusOf(t, f.thin); thinAfter != thinBefore {
					t.Fatalf("refused Bind changed the context:\nbefore %+v\nafter  %+v", thinBefore, thinAfter)
				}
				if tc.check != nil {
					tc.check(t, f, sourceBefore)
				}
				return
			}
			if err != nil {
				t.Fatalf("Bind: %v", err)
			}
			if result.Changed != tc.wantChanged {
				t.Fatalf("Changed = %v, want %v", result.Changed, tc.wantChanged)
			}
			if canonical(t, result.Context) != canonical(t, f.thinCommon) {
				t.Fatalf("Context = %q, want %q", result.Context, f.thinCommon)
			}
			if want := bindingFor(t, wantCommon); result.Binding != want {
				t.Fatalf("Binding = %+v, want %+v", result.Binding, want)
			}
			if resolved := canonical(t, result.Binding.CommonDir); resolved != result.Binding.CommonDir {
				t.Fatalf("stored path %q is not canonical (%q)", result.Binding.CommonDir, resolved)
			}
			values := strings.Split(gittest.Git(t, f.thin, "config", "--file", f.config(), "--get-all", bindingKey), "\n")
			if len(values) != 1 || values[0] != result.Binding.String() {
				t.Fatalf("context config holds %q, want exactly %q", values, result.Binding.String())
			}
			s, err := Open(f.worktreeA)
			if err != nil {
				t.Fatalf("Open after Bind: %v", err)
			}
			if s.RecordsCommonDir() != wantCommon {
				t.Fatalf("Open after Bind: RecordsCommonDir = %q, want %q", s.RecordsCommonDir(), wantCommon)
			}
			if tc.check != nil {
				tc.check(t, f, sourceBefore)
			}
			f.assertNoRecords(t)
		})
	}

	t.Run("source is not a repository", func(t *testing.T) {
		f := newSharedFixture(t)
		_, err := Bind(t.Context(), f.thin, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "open source repository") {
			t.Fatalf("Bind = %v, want an open source repository error", err)
		}
		var be *BindingError
		if errors.As(err, &be) {
			t.Fatalf("a missing source is a plain error, got %v", be)
		}
	})

	t.Run("inherited GIT_DIR cannot substitute the source", func(t *testing.T) {
		f := newSharedFixture(t)
		decoy := initSourceRepo(t)
		t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
		result, err := Bind(t.Context(), f.thin, f.source)
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if result.Binding.CommonDir != f.sourceCommon {
			t.Fatalf("Bind followed GIT_DIR to %q, want %q", result.Binding.CommonDir, f.sourceCommon)
		}
	})
}
