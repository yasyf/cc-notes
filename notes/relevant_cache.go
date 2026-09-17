package notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/cc-notes/model"
)

const relevantRacyWindow = 2 * time.Second

type relevantCacheEntry struct {
	Key      string      `json:"key"`
	Deadline int64       `json:"deadline,omitempty"`
	Files    []fileStamp `json:"files,omitempty"`
	Output   string      `json:"output"`
}

type fileStamp struct {
	Path    string      `json:"path"`
	Missing bool        `json:"missing,omitempty"`
	Size    int64       `json:"size,omitempty"`
	ModTime int64       `json:"mtime,omitempty"`
	Ctime   int64       `json:"ctime,omitempty"`
	Inode   uint64      `json:"inode,omitempty"`
	Mode    os.FileMode `json:"mode,omitempty"`
}

// RelevantCached renders the Relevant result for target through render,
// serving it from the repository's relevance cache when nothing the result
// depends on has changed. variant names everything render bakes into its
// output (format, limit), so two renderings never share an entry.
//
// An entry is keyed on every input Relevant reads: this binary, the worktree
// and working directory, the target and filter, HEAD and the symbolic refs
// behind the branch and default-branch lookups, every ref tip except the sync
// tracking namespace, the shallow boundary, the author identity, and the
// staleness threshold. Two inputs change without touching any of those and are
// validated at read time instead: the clock, which turns a fresh verdict stale
// at a known instant, and, under Worktree, the on-disk content of the path
// anchors whose drift was checked. Each such file is fingerprinted (existence,
// size, mtime, ctime, inode, mode) before and after the drift check reads it,
// and the result is cached only when both fingerprints agree and no mtime falls
// within the racy window of the computation.
func (c *Client) RelevantCached(ctx context.Context, target string, filter RelevantFilter, variant string, render func([]RelevantEntry) ([]byte, error)) ([]byte, error) {
	p, err := c.relevantPath(ctx, target)
	if err != nil {
		return nil, err
	}
	key, staleAfter, err := c.relevantCacheKey(ctx, p, filter, variant)
	if err != nil {
		return nil, err
	}
	name := relevantCacheName(c.s.GitDir(), c.s.Git.Dir, p, filter, variant)
	start := time.Now()
	if data, ok := c.s.ReadRelevantCache(name); ok {
		var entry relevantCacheEntry
		if json.Unmarshal(data, &entry) == nil && entry.Key == key && entry.valid(c.s.Git.Dir, start) {
			return []byte(entry.Output), nil
		}
	}
	entries, clock, err := c.relevantScored(ctx, p, filter)
	if err != nil {
		return nil, err
	}
	var paths []string
	if filter.Worktree {
		paths = driftCheckedPaths(entries)
	}
	before := stampsOf(c.s.Git.Dir, paths)
	if err := c.relevantVerdicts(ctx, entries, clock, filter.Worktree); err != nil {
		return nil, err
	}
	out, err := render(entries)
	if err != nil {
		return nil, err
	}
	after := stampsOf(c.s.Git.Dir, paths)
	if !slices.Equal(before, after) || slices.ContainsFunc(after, func(f fileStamp) bool { return f.racy(start) }) {
		return out, nil
	}
	entry := relevantCacheEntry{Key: key, Files: after, Output: string(out)}
	for _, e := range entries {
		if fe, ok := freshOf(e); ok && e.Verdict == "" && fe.StaleAt == 0 && fe.VerifiedAt != 0 {
			deadline := time.Unix(fe.VerifiedAt, 0).Add(staleAfter).UnixNano()
			if entry.Deadline == 0 || deadline < entry.Deadline {
				entry.Deadline = deadline
			}
		}
	}
	if data, err := json.Marshal(entry); err == nil {
		c.s.WriteRelevantCache(name, data)
	}
	return out, nil
}

func driftCheckedPaths(entries []RelevantEntry) []string {
	var paths []string
	for _, e := range entries {
		fe, ok := freshOf(e)
		if !ok || fe.StaleAt != 0 || fe.VerifiedAt == 0 {
			continue
		}
		for _, a := range fe.Anchors {
			if a.Kind == model.AnchorPath && !slices.Contains(paths, a.Value) {
				paths = append(paths, a.Value)
			}
		}
	}
	return paths
}

func stampsOf(dir string, paths []string) []fileStamp {
	stamps := make([]fileStamp, len(paths))
	for i, p := range paths {
		stamps[i] = stampOf(dir, p)
	}
	return stamps
}

func (f fileStamp) racy(start time.Time) bool {
	return !f.Missing && time.Unix(0, f.ModTime).After(start.Add(-relevantRacyWindow))
}

func (c *Client) relevantCacheKey(ctx context.Context, p string, filter RelevantFilter, variant string) (string, time.Duration, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", 0, err
	}
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return "", 0, err
	}
	head, err := c.head(ctx)
	if err != nil {
		return "", 0, err
	}
	refs, err := c.s.Repo.ListPrefix(ctx, "refs/")
	if err != nil {
		return "", 0, err
	}
	authorName, authorEmail, err := c.s.Git.AuthorIdent(ctx)
	if err != nil {
		return "", 0, err
	}
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s %d %d\n%s\n%s %s\n%s\n", relevantCacheName(c.s.GitDir(), c.s.Git.Dir, p, filter, variant), version.Version, exeInfo.Size(), exeInfo.ModTime().UnixNano(), head, authorName, authorEmail, staleAfter)
	for _, file := range []string{
		filepath.Join(c.s.GitDir(), "HEAD"),
		filepath.Join(c.s.CommonDir(), "refs", "remotes", "origin", "HEAD"),
		filepath.Join(c.s.CommonDir(), "shallow"),
	} {
		data, _ := os.ReadFile(file) //nolint:gosec // G304: fixed paths inside this repository's git directories.
		fmt.Fprintf(h, "%s %q\n", file, data)
	}
	names := make([]string, 0, len(refs))
	for ref := range refs {
		if !strings.HasPrefix(ref, "refs/cc-notes-sync/") {
			names = append(names, ref)
		}
	}
	slices.Sort(names)
	for _, ref := range names {
		fmt.Fprintf(h, "%s %s\n", ref, refs[ref])
	}
	return hex.EncodeToString(h.Sum(nil)), staleAfter, nil
}

func relevantCacheName(gitDir, dir, p string, filter RelevantFilter, variant string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\n%s\n%s\n%+v\n%s", gitDir, dir, p, filter, variant))
	return hex.EncodeToString(sum[:])
}

func (e relevantCacheEntry) valid(dir string, now time.Time) bool {
	if e.Deadline != 0 && now.UnixNano() > e.Deadline {
		return false
	}
	for _, f := range e.Files {
		if stampOf(dir, f.Path) != f {
			return false
		}
	}
	return true
}

func stampOf(dir, path string) fileStamp {
	info, err := os.Stat(filepath.Join(dir, path))
	if err != nil {
		return fileStamp{Path: path, Missing: true}
	}
	ctime, inode := statIdentity(info)
	return fileStamp{Path: path, Size: info.Size(), ModTime: info.ModTime().UnixNano(), Ctime: ctime, Inode: inode, Mode: info.Mode()}
}

func freshOf(e RelevantEntry) (freshDocument, bool) {
	switch e.Kind {
	case model.KindDoc:
		return freshFromDoc(e.Doc), true
	case model.KindAnswer:
		return freshFromAnswer(e.Answer), true
	case model.KindNote:
		return freshFromNote(e.Note), true
	default:
		return freshDocument{}, false
	}
}
