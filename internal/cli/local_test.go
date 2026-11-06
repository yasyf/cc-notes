package cli_test

import (
	"fmt"
	"testing"
)

func keptLocalNotice(id string) string {
	return fmt.Sprintf("cc-notes: kept local by default: log %s, attachment trace.log matches *.log; no other clone can read it until you publish it with `ccn local unmark %s`\n", id[:7], id[:7])
}

// TestWriteAnnouncesDefaultLocal pins that a write a local default keeps on this
// clone says so once on stderr, while a plain, --sync, or --local write says
// nothing: only the default is a surprise to the writer.
func TestWriteAnnouncesDefaultLocal(t *testing.T) {
	dir := initRepo(t)
	seed, _ := writeAttachable(t, "seed.txt", []byte("seed"))
	mustRun(t, dir, "note", "add", "Seed", "--attach", seed)
	raw, _ := writeAttachable(t, "trace.log", []byte("raw capture"))
	plain, _ := writeAttachable(t, "excerpt.txt", []byte("excerpt"))

	for _, tc := range []struct {
		name   string
		args   []string
		notice bool
	}{
		{"default glob", []string{"--entry", "first", "--attach", raw}, true},
		{"plain attachment", []string{"--attach", plain}, false},
		{"synced", []string{"--attach", raw, "--sync"}, false},
		{"explicit local", []string{"--attach", raw, "--local"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := runCLI(t, dir, append([]string{"log", "add", tc.name, "--json"}, tc.args...)...)
			if err != nil {
				t.Fatalf("log add: %v (stderr %q)", err, stderr)
			}
			want := ""
			if tc.notice {
				want = keptLocalNotice(mustJSON[logJSON](t, stdout).ID)
			}
			if stderr != want {
				t.Fatalf("stderr = %q, want %q", stderr, want)
			}
		})
	}
}

// TestAppendAnnouncesDefaultLocal pins that an append whose attachment brings
// an unpublished entity under a local default announces it.
func TestAppendAnnouncesDefaultLocal(t *testing.T) {
	dir := initRepo(t)
	seed, _ := writeAttachable(t, "seed.txt", []byte("seed"))
	mustRun(t, dir, "note", "add", "Seed", "--attach", seed)
	added := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "Evidence", "--json"))
	raw, _ := writeAttachable(t, "trace.log", []byte("raw capture"))

	_, stderr, err := runCLI(t, dir, "log", "append", added.ID, "--attach", raw)
	if err != nil {
		t.Fatalf("log append: %v (stderr %q)", err, stderr)
	}
	if want := keptLocalNotice(added.ID); stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}
