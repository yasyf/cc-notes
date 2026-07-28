package stale

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

// minIdentLen is the shortest run S8 will treat as a code reference.
const minIdentLen = 3

// minHexLen is the shortest run that can be a sha or entity-id prefix.
const minHexLen = 7

// Tree is the set of identifier tokens present in the repository's tracked text
// files — the oracle S8 exact-matches a record's code references against.
//
// The signal it feeds catches deletion only. An identifier that still exists
// but now behaves differently matches and passes silently, so S8 buys precision
// at recall's expense by design; it is not a semantic-drift detector.
type Tree struct {
	idents map[string]struct{}
}

// ScanTree tokenizes every tracked text file in g's repository into the
// identifier set. Files above maxBytes, files holding a NUL byte, and tracked
// entries that are not regular files — symlinks and submodule gitlinks — are
// skipped: a binary blob contributes only noise, a symlink's target is either
// tracked in its own right or outside the tree, and an identifier the scan
// misses costs S8 a flag it should have raised, never one it should not have.
func ScanTree(ctx context.Context, g gitcmd.Git, maxBytes int64) (*Tree, error) {
	root, err := g.Root(ctx)
	if err != nil {
		return nil, err
	}
	tracked, err := g.TrackedFiles(ctx)
	if err != nil {
		return nil, err
	}
	t := &Tree{idents: map[string]struct{}{}}
	for _, name := range tracked {
		full := filepath.Join(root, name)
		info, err := os.Lstat(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > maxBytes {
			continue
		}
		//nolint:gosec // G304: full is a path git itself reported as tracked in the repository under scan, not external input.
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		text := string(data)
		if strings.ContainsRune(text, 0) {
			continue
		}
		for _, run := range identRuns(text) {
			t.idents[run] = struct{}{}
		}
	}
	return t, nil
}

// Has reports whether the identifier appears anywhere in the scanned tree.
func (t *Tree) Has(ident string) bool {
	_, ok := t.idents[ident]
	return ok
}

// Size is the number of distinct identifiers the scan indexed.
func (t *Tree) Size() int { return len(t.idents) }

// DeadRefs returns the sorted code references a record's text names that the
// tree no longer holds.
func (t *Tree) DeadRefs(text string) []string {
	var out []string
	for _, c := range Candidates(text) {
		if !t.Has(c) {
			out = append(out, c)
		}
	}
	return out
}

// backtickSpan matches a markdown code span or fenced block: the backticks are
// the author's own evidence that what they delimit is code.
var backtickSpan = regexp.MustCompile("`+([^`]+)`+")

// Candidates extracts the code elements a record's text names, sorted and
// deduplicated. Inside a backtick span every identifier counts; in free prose
// only a camelCase hump or a snake_case underscore does, which is what keeps
// S8's flags precise.
func Candidates(text string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(run string) {
		if _, ok := seen[run]; ok {
			return
		}
		seen[run] = struct{}{}
		out = append(out, run)
	}
	for _, span := range backtickSpan.FindAllStringSubmatch(text, -1) {
		for _, run := range identRuns(span[1]) {
			if plausible(run) {
				add(run)
			}
		}
	}
	for _, run := range identRuns(text) {
		if plausible(run) && codeShaped(run) {
			add(run)
		}
	}
	slices.Sort(out)
	return out
}

// plausible drops the runs no code element can be: too short, digit-led, or a
// hex blob — an abbreviated commit sha or entity id, which records cite often
// and the tree never contains.
func plausible(s string) bool {
	if len(s) < minIdentLen || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	return !looksHex(s)
}

// looksHex reports whether a run is a lowercase hex blob carrying at least one
// digit — the shape of a sha or an entity id prefix.
func looksHex(s string) bool {
	if len(s) < minHexLen {
		return false
	}
	digit := false
	for i := range len(s) {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return digit
}

// identRuns splits text into maximal runs of identifier characters, so a
// qualified reference like `notes.Client` yields both segments.
func identRuns(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// codeShaped reports whether a run carries a camelCase hump — an uppercase
// letter following a lowercase one or a digit — or a snake_case underscore
// between two other characters.
func codeShaped(s string) bool {
	lowerBefore, snake := false, false
	for i := range len(s) {
		switch c := s[i]; {
		case c == '_':
			snake = snake || (i > 0 && i < len(s)-1)
			lowerBefore = false
		case c >= 'A' && c <= 'Z':
			if lowerBefore {
				return true
			}
		default:
			lowerBefore = true
		}
	}
	return snake
}
