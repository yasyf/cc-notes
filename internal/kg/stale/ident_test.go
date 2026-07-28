package stale

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestCandidates(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"a camelCase hump qualifies in prose", "verdictOf resolves it", []string{"verdictOf"}},
		{"a PascalCase compound qualifies in prose", "EntityID is the key", []string{"EntityID"}},
		{"snake_case qualifies in prose", "the note_verify surface", []string{"note_verify"}},
		{"a single PascalCase word needs backticks", "Widget spins", nil},
		{"a backticked single word qualifies", "the `Widget` spins", []string{"Widget"}},
		{"a qualified reference splits into segments", "call `notes.Client` here", []string{"Client", "notes"}},
		{"ALL CAPS prose is not a code element", "READ THE DOCS NOW", nil},
		{"an env var is snake-shaped", "set CC_NOTES_ACTOR first", []string{"CC_NOTES_ACTOR"}},
		{"a short run is dropped", "`ok` and `id`", nil},
		{"a sha prefix is not a code element", "landed in `f62eca0` and `48baca6`", nil},
		{"a hex-looking word with no digit survives", "the `decade` field", []string{"decade"}},
		{"a fenced block is code", "```\nfor _, ent := range corpus {\n```", []string{"corpus", "ent", "for", "range"}},
		{"duplicates fold", "verdictOf and verdictOf again", []string{"verdictOf"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Candidates(tc.text); !slices.Equal(got, tc.want) {
				t.Errorf("Candidates(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestScanTreeIndexesTrackedIdentifiers(t *testing.T) {
	_, dir := openRepo(t)
	tree, err := ScanTree(t.Context(), gitcmd.Git{Dir: dir}, MaxScanBytes)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	for _, ident := range []string{"Widget", "Spin", "package", "pkg"} {
		if !tree.Has(ident) {
			t.Errorf("Has(%q) = false, want the committed source indexed", ident)
		}
	}
	if tree.Has("Rotor") {
		t.Error(`Has("Rotor") = true, want false — nothing in the tree defines it`)
	}
	if tree.Size() == 0 {
		t.Error("Size() = 0, want the committed source indexed")
	}
}

// TestScanTreeSkipsSymlinkToDirectory reproduces the real-corpus crash: a
// tracked symlink whose target is a directory (`.claude/skills -> ../.agents/skills`)
// made the stat-then-read scan fail with "is a directory".
func TestScanTreeSkipsSymlinkToDirectory(t *testing.T) {
	_, dir := openRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "shared", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "shared/skills/rotor.go", "package skills\n\nfunc RotorPhase() {}\n")
	if err := os.Symlink(filepath.Join("shared", "skills"), filepath.Join(dir, "linked")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "symlink a directory")

	tree, err := ScanTree(t.Context(), gitcmd.Git{Dir: dir}, MaxScanBytes)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if !tree.Has("RotorPhase") {
		t.Error(`Has("RotorPhase") = false, want the symlink's real target indexed through its own tracked path`)
	}
}

func TestDeadRefsFlagDeletedElementsOnly(t *testing.T) {
	_, dir := openRepo(t)
	tree, err := ScanTree(t.Context(), gitcmd.Git{Dir: dir}, MaxScanBytes)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	got := tree.DeadRefs("`Widget.Spin` is fine; `Widget.Stall` and rotorPhase are gone")
	want := []string{"Stall", "rotorPhase"}
	if !slices.Equal(got, want) {
		t.Errorf("DeadRefs = %v, want %v", got, want)
	}
}

// TestDeadRefsMissSemanticDrift pins the documented limit: S8 detects deletion,
// not behavior change. An identifier that still exists passes silently however
// far its meaning has moved.
func TestDeadRefsMissSemanticDrift(t *testing.T) {
	_, dir := openRepo(t)
	tree, err := ScanTree(t.Context(), gitcmd.Git{Dir: dir}, MaxScanBytes)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if got := tree.DeadRefs("`Widget.Spin` returns the tick count now, not one"); len(got) != 0 {
		t.Errorf("DeadRefs = %v, want none — S8 cannot see a live identifier's meaning change", got)
	}
}

func TestCodeShaped(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"verdictOf", true},
		{"EntityID", true},
		{"note_verify", true},
		{"CC_NOTES_ACTOR", true},
		{"Widget", false},
		{"widget", false},
		{"SHA", false},
		{"_leading", false},
		{"trailing_", false},
		{"sha256", false},
		{"utf8Decode", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := codeShaped(tc.in); got != tc.want {
				t.Errorf("codeShaped(%q) = %t, want %t", tc.in, got, tc.want)
			}
		})
	}
}
