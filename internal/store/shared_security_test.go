package store

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
)

// shimGit puts a git wrapper first on PATH: when "$*" matches pattern (an sh
// case glob) it runs interleave through the real git first, then execs the
// real git with the original argv. Tests use it to land a concurrent write
// between two steps of one operation.
func shimGit(t *testing.T, pattern string, interleave ...string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\ncase \"$*\" in\n%s) %s", pattern, quote(real))
	for _, arg := range interleave {
		b.WriteString(" " + quote(arg))
	}
	fmt.Fprintf(&b, " ;;\nesac\nexec %s \"$@\"\n", quote(real))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(b.String()), 0o700); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func symlinkOver(t *testing.T, path, target string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink %s -> %s: %v", path, target, err)
	}
}

// TestCheckRecordsBackendRouting pins S3: a bound store's recheck refuses a
// backend that started redirecting after open, through a commondir file or a
// binding in its own config, and re-reads the backend config only when its
// stamp moved.
func TestCheckRecordsBackendRouting(t *testing.T) {
	rows := []struct {
		name     string
		mutate   func(t *testing.T, f *sharedFixture)
		sentinel error
		// openSentinel is what a fresh Open reports; validateBackend classifies
		// a commondir file as a layout failure, the recheck as a redirect.
		openSentinel error
	}{
		{
			name: "backend gains a commondir file",
			mutate: func(t *testing.T, f *sharedFixture) {
				if err := os.WriteFile(filepath.Join(f.sourceCommon, "commondir"), []byte("../elsewhere\n"), 0o600); err != nil {
					t.Fatalf("write commondir: %v", err)
				}
			},
			sentinel:     ErrBackendRedirects,
			openSentinel: ErrBackendUnavailable,
		},
		{
			name: "backend becomes bound",
			mutate: func(t *testing.T, f *sharedFixture) {
				setBindingValue(t, filepath.Join(f.sourceCommon, "config"), bindingFor(t, filepath.Join(initSourceRepo(t), ".git")).String())
			},
			sentinel: ErrBackendRedirects,
		},
		{
			name: "backend gains an unparsable binding",
			mutate: func(t *testing.T, f *sharedFixture) {
				setBindingValue(t, filepath.Join(f.sourceCommon, "config"), `{nope`)
			},
			sentinel: ErrBackendRedirects,
		},
		{
			name: "unrelated backend config change",
			mutate: func(t *testing.T, f *sharedFixture) {
				gittest.Git(t, f.source, "config", "cc-notes.probe", "changed")
			},
		},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			f.bind(t)
			s, err := Open(f.worktreeA)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := s.CheckRecords(); err != nil {
				t.Fatalf("CheckRecords before the change: %v", err)
			}
			tc.mutate(t, f)
			for call := range 2 {
				err := s.CheckRecords()
				if tc.sentinel == nil {
					if err != nil {
						t.Fatalf("CheckRecords call %d = %v, want nil", call, err)
					}
					continue
				}
				assertBindingError(t, err, tc.sentinel, f.config(), f.sourceCommon)
			}
			want := tc.sentinel
			if tc.openSentinel != nil {
				want = tc.openSentinel
			}
			if _, err := Open(f.worktreeB); (err == nil) != (want == nil) || (err != nil && !errors.Is(err, want)) {
				t.Fatalf("Open after the change = %v, want %v", err, want)
			}
		})
	}
}

// TestBackendSymlinkedLayoutRedirects pins S7: a backend whose refs, objects
// or HEAD is a symlink into another repository is refused at open, and at bind
// where git's own discovery still accepts the source (git refuses a symlinked
// HEAD outright, so that row never reaches validateBackend through Bind).
func TestBackendSymlinkedLayoutRedirects(t *testing.T) {
	for _, entry := range []string{"refs", "objects", "HEAD"} {
		t.Run(entry, func(t *testing.T) {
			f := newSharedFixture(t)
			victim := initSourceRepo(t)
			symlinkOver(t, filepath.Join(f.sourceCommon, entry), filepath.Join(victim, ".git", entry))
			victimBefore := censusOf(t, victim)

			_, err := Bind(t.Context(), f.thin, f.source)
			if entry == "HEAD" {
				var be *BindingError
				if err == nil || errors.As(err, &be) {
					t.Fatalf("Bind = %v, want git to refuse discovering the source", err)
				}
			} else {
				be := assertBindingError(t, err, ErrBackendRedirects, f.config(), f.sourceCommon)
				if !strings.Contains(be.Error(), filepath.Join(f.sourceCommon, entry)) {
					t.Fatalf("refusal %q does not name the symlinked entry", be)
				}
			}
			if _, bound, err := readBinding(f.config()); err != nil || bound {
				t.Fatalf("refused Bind published a binding (bound %v, %v)", bound, err)
			}

			setBindingValue(t, f.config(), bindingFor(t, f.sourceCommon).String())
			_, err = Open(f.worktreeA)
			be := assertBindingError(t, err, ErrBackendRedirects, f.config(), f.sourceCommon)
			if !strings.Contains(be.Error(), filepath.Join(f.sourceCommon, entry)) {
				t.Fatalf("refusal %q does not name the symlinked entry", be)
			}
			if after := censusOf(t, victim); after != victimBefore {
				t.Fatalf("victim changed:\nbefore %+v\nafter  %+v", victimBefore, after)
			}
			f.assertNoRecords(t)
		})
	}
}

// TestBindIdempotentRevalidatesBackend pins S8: a context already carrying the
// binding Bind would publish is accepted only after the backend validates.
func TestBindIdempotentRevalidatesBackend(t *testing.T) {
	rows := []struct {
		name     string
		sabotage func(t *testing.T, backend string)
		sentinel error
	}{
		{
			name: "backend removed",
			sabotage: func(t *testing.T, backend string) {
				if err := os.RemoveAll(backend); err != nil {
					t.Fatalf("remove backend: %v", err)
				}
			},
			sentinel: ErrBackendUnavailable,
		},
		{
			name:     "backend replaced",
			sabotage: func(t *testing.T, backend string) { replaceRepo(t, backend) },
			sentinel: ErrBackendReplaced,
		},
		{
			name: "backend bound onward",
			sabotage: func(t *testing.T, backend string) {
				setBindingValue(t, filepath.Join(backend, ".git", "config"), bindingFor(t, filepath.Join(initSourceRepo(t), ".git")).String())
			},
			sentinel: ErrBackendRedirects,
		},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			backend := initSourceRepo(t)
			backendCommon := canonical(t, filepath.Join(backend, ".git"))
			if _, err := Bind(t.Context(), f.source, backend); err != nil {
				t.Fatalf("bind source to backend: %v", err)
			}
			first, err := Bind(t.Context(), f.thin, f.source)
			if err != nil || first.Binding.CommonDir != backendCommon {
				t.Fatalf("bind context through source = %+v, %v; want backend %s", first, err, backendCommon)
			}
			if again, err := Bind(t.Context(), f.thin, f.source); err != nil || again.Changed {
				t.Fatalf("rebind with a valid backend = %+v, %v; want idempotent", again, err)
			}
			tc.sabotage(t, backend)
			_, err = Bind(t.Context(), f.thin, f.source)
			assertBindingError(t, err, tc.sentinel, f.config(), backendCommon)
		})
	}
}

// TestBindRefusesSymlinkedContextConfig pins S6: a context config that is a
// symlink is refused before anything is written, and the file it points at
// stays byte-identical.
func TestBindRefusesSymlinkedContextConfig(t *testing.T) {
	f := newSharedFixture(t)
	symlinkOver(t, f.config(), filepath.Join(f.sourceCommon, "config"))
	sourceBefore := censusOf(t, f.source)

	_, err := Bind(t.Context(), f.thin, f.source)
	var be *BindingError
	if !errors.Is(err, ErrBindingMalformed) || !errors.As(err, &be) {
		t.Fatalf("Bind = %T %v, want *BindingError wrapping ErrBindingMalformed", err, err)
	}
	if be.Config != f.config() || !strings.Contains(be.Error(), "not a regular file") {
		t.Fatalf("refusal %q does not name the symlinked config %s", be, f.config())
	}
	if sourceAfter := censusOf(t, f.source); sourceAfter != sourceBefore {
		t.Fatalf("Bind wrote through the symlink into the source:\nbefore %+v\nafter  %+v", sourceBefore, sourceAfter)
	}
	if info, err := os.Lstat(f.config()); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("context config is no longer the symlink: %v, %v", info, err)
	}
	if _, err := os.Lstat(f.config() + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a lock was left behind: %v", err)
	}
}

// TestBindRefusesHeldConfigLock pins S5(a): a config.lock another process holds
// refuses the bind outright, is never removed, and nothing is published.
func TestBindRefusesHeldConfigLock(t *testing.T) {
	f := newSharedFixture(t)
	lock := f.config() + ".lock"
	if err := os.WriteFile(lock, []byte("held\n"), 0o600); err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	before := censusOf(t, f.thin)

	_, err := Bind(t.Context(), f.thin, f.source)
	if err == nil || !strings.Contains(err.Error(), lock) {
		t.Fatalf("Bind = %v, want a refusal naming %s", err, lock)
	}
	var be *BindingError
	if errors.As(err, &be) {
		t.Fatalf("a held lock is a plain error, got %v", be)
	}
	if after := censusOf(t, f.thin); after != before {
		t.Fatalf("Bind changed the context under a held lock:\nbefore %+v\nafter  %+v", before, after)
	}
	held, err := os.ReadFile(lock)
	if err != nil || string(held) != "held\n" {
		t.Fatalf("Bind disturbed a lock it did not take: %q, %v", held, err)
	}
}

// TestBindRollsBackWhenRecordAppears pins S5(b): a record that lands in the
// context between the emptiness probe and the publish is caught by the
// post-publish probe; the binding is removed again and the refusal names the
// ref, so the record is never hidden.
func TestBindRollsBackWhenRecordAppears(t *testing.T) {
	f := newSharedFixture(t)
	const raced = "refs/cc-notes/notes/raced"
	shimGit(t, `*"config --file "*"cc-notes.storage"*`, "-C", f.thin, "update-ref", raced, "HEAD")

	_, err := Bind(t.Context(), f.thin, f.source)
	be := assertBindingError(t, err, ErrContextHasRecords, f.config(), f.sourceCommon)
	if !strings.Contains(be.Error(), raced) {
		t.Fatalf("refusal %q does not name %s", be, raced)
	}
	if _, bound, err := readBinding(f.config()); err != nil || bound {
		t.Fatalf("binding left published after the rollback (bound %v, %v)", bound, err)
	}
	if _, err := os.Lstat(f.config() + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a lock was left behind: %v", err)
	}
	s, err := Open(f.thin)
	if err != nil {
		t.Fatalf("Open after the rollback: %v", err)
	}
	if _, bound := s.Binding(); bound {
		t.Fatal("store opened bound after the rollback")
	}
	if got := gittest.Git(t, f.thin, "for-each-ref", "--format=%(refname)", "refs/cc-notes/"); got != raced {
		t.Fatalf("context refs = %q, want exactly %s", got, raced)
	}
}

// TestPublishRefRechecksBinding pins S5(c): a binding published while a
// records ref was being written fails the write visibly, naming the ref,
// instead of leaving the record hidden behind the new binding.
func TestPublishRefRechecksBinding(t *testing.T) {
	f := newSharedFixture(t)
	s, err := Open(f.thin)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.CheckRecords(); err != nil {
		t.Fatalf("CheckRecords: %v", err)
	}
	shimGit(t, `*"update-ref --stdin"*`, "config", "--file", f.config(), bindingKey, bindingFor(t, f.sourceCommon).String())

	_, err = s.Create(t.Context(), noteOps("raced"))
	assertBindingError(t, err, ErrBindingChanged, f.config(), "")
	refs := gittest.Git(t, f.thin, "for-each-ref", "--format=%(refname)", "refs/cc-notes/notes/")
	if refs == "" || strings.Contains(refs, "\n") {
		t.Fatalf("context refs = %q, want exactly the one published ref", refs)
	}
	if !strings.Contains(err.Error(), refs) {
		t.Fatalf("error %q does not name the published ref %s", err, refs)
	}
}

func TestReplaceConfig(t *testing.T) {
	t.Run("edit failure removes the lock and keeps config", func(t *testing.T) {
		config := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(config, []byte("[a]\n\tb = c\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("boom")
		err := replaceConfig(config, 0o600, func(lock string) error {
			if data, err := os.ReadFile(lock); err != nil || string(data) != "[a]\n\tb = c\n" {
				t.Fatalf("lock seeded with %q, %v", data, err)
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("replaceConfig = %v, want boom", err)
		}
		if _, err := os.Lstat(config + ".lock"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock left behind: %v", err)
		}
		if data, _ := os.ReadFile(config); string(data) != "[a]\n\tb = c\n" {
			t.Fatalf("config changed to %q", data)
		}
	})
	t.Run("success renames the edited lock over config", func(t *testing.T) {
		config := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(config, []byte("old\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		err := replaceConfig(config, 0o640, func(lock string) error { return os.WriteFile(lock, []byte("new\n"), 0o600) })
		if err != nil {
			t.Fatalf("replaceConfig: %v", err)
		}
		data, err := os.ReadFile(config)
		if err != nil || string(data) != "new\n" {
			t.Fatalf("config = %q, %v; want new", data, err)
		}
		if _, err := os.Lstat(config + ".lock"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock left behind: %v", err)
		}
	})
	t.Run("symlinked config is not followed", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		config := filepath.Join(dir, "config")
		if err := os.Symlink(target, config); err != nil {
			t.Fatal(err)
		}
		err := replaceConfig(config, 0o600, func(string) error { t.Fatal("edit ran on a symlinked config"); return nil })
		if err == nil {
			t.Fatal("replaceConfig followed the symlink")
		}
		if data, _ := os.ReadFile(target); string(data) != "secret\n" {
			t.Fatalf("target changed to %q", data)
		}
		if _, err := os.Lstat(config + ".lock"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock left behind: %v", err)
		}
	})
}
