package notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
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
// and working directory, the target and filter, the commit a --base revision
// resolves to, HEAD, every symbolic ref's target, every ref tip except the sync
// tracking namespace, the shallow boundary, the staleness threshold, the
// GIT_* environment, and `git var -l`, which carries the author identity, the
// config and attribute file locations, and the effective configuration of
// every scope with its includes resolved. Two inputs change without touching any of those and are
// validated at read time instead: the clock, which turns a fresh verdict stale
// at a known instant, and, under Worktree, the on-disk content of the path
// anchors whose drift was checked, with the gitattributes files that shape how
// git hashes them. Each such file is fingerprinted (existence, size, mtime,
// ctime, inode, mode) before and after the drift check reads it,
// and the result is cached only when both fingerprints agree and no mtime falls
// within the racy window of the computation.
func (c *Client) RelevantCached(ctx context.Context, target string, filter RelevantFilter, variant string, render func([]RelevantEntry) ([]byte, error)) ([]byte, error) {
	p, err := c.relevantPath(ctx, target)
	if err != nil {
		return nil, err
	}
	key, staleAfter, vars, err := c.relevantCacheKey(ctx, p, filter, variant)
	if err != nil {
		return nil, err
	}
	name := relevantCacheName(c.s.GitDir(), c.s.Git.Dir, p, filter, variant)
	start := time.Now()
	if data, ok := c.s.ReadRelevantCache(name); ok {
		var entry relevantCacheEntry
		if json.Unmarshal(data, &entry) == nil && entry.Key == key && entry.valid(start) {
			return []byte(entry.Output), nil
		}
	}
	entries, clock, err := c.relevantScored(ctx, p, filter)
	if err != nil {
		return nil, err
	}
	var paths []string
	if filter.Worktree {
		if paths, err = c.driftInputs(ctx, entries, vars); err != nil {
			return nil, err
		}
	}
	before := stampsOf(paths)
	if err := c.relevantVerdicts(ctx, entries, clock, filter.Worktree); err != nil {
		return nil, err
	}
	out, err := render(entries)
	if err != nil {
		return nil, err
	}
	after := stampsOf(paths)
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

func (c *Client) driftInputs(ctx context.Context, entries []RelevantEntry, vars map[string]string) ([]string, error) {
	var paths []string
	add := func(p string) {
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	root, err := c.s.Root(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		fe, ok := freshOf(e)
		if !ok || fe.StaleAt != 0 || fe.VerifiedAt == 0 {
			continue
		}
		for _, a := range fe.Anchors {
			if a.Kind != model.AnchorPath {
				continue
			}
			file := filepath.Join(root, a.Value)
			add(file)
			for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
				add(filepath.Join(dir, ".gitattributes"))
				if dir == root || dir == filepath.Dir(dir) {
					break
				}
			}
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	add(filepath.Join(c.s.CommonDir(), "info", "attributes"))
	for _, v := range []string{"GIT_ATTR_GLOBAL", "GIT_ATTR_SYSTEM"} {
		if p := vars[v]; p != "" {
			add(p)
		}
	}
	return paths, nil
}

func stampsOf(paths []string) []fileStamp {
	stamps := make([]fileStamp, len(paths))
	for i, p := range paths {
		stamps[i] = stampOf(p)
	}
	return stamps
}

func (f fileStamp) racy(start time.Time) bool {
	return !f.Missing && time.Unix(0, f.ModTime).After(start.Add(-relevantRacyWindow))
}

func (c *Client) relevantCacheKey(ctx context.Context, p string, filter RelevantFilter, variant string) (string, time.Duration, map[string]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", 0, nil, err
	}
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return "", 0, nil, err
	}
	head, err := c.head(ctx)
	if err != nil {
		return "", 0, nil, err
	}
	refs, err := c.s.Repo.ListPrefix(ctx, "refs/")
	if err != nil {
		return "", 0, nil, err
	}
	symbolic, err := c.s.Repo.ListSymbolic(ctx, "refs/")
	if err != nil {
		return "", 0, nil, err
	}
	var base model.SHA
	if filter.Base != "" {
		base, err = c.s.Git.CommitSHA(ctx, filter.Base)
		if err != nil && !errors.Is(err, gitcmd.ErrRevNotFound) {
			return "", 0, nil, err
		}
	}
	varList, err := c.s.Git.VarList(ctx)
	if err != nil {
		return "", 0, nil, err
	}
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		return "", 0, nil, err
	}
	vars := make(map[string]string)
	h := sha256.New()
	if _, err := fmt.Fprintf(h, "%s\n%s %d %d\n%s\nbase %s\n%s\n", relevantCacheName(c.s.GitDir(), c.s.Git.Dir, p, filter, variant), version.Version, exeInfo.Size(), exeInfo.ModTime().UnixNano(), head, base, staleAfter); err != nil {
		return "", 0, nil, err
	}
	for _, line := range strings.Split(varList, "\n") {
		name, value, _ := strings.Cut(line, "=")
		if name == "GIT_AUTHOR_IDENT" || name == "GIT_COMMITTER_IDENT" {
			value = value[:strings.LastIndexByte(value, '>')+1]
		}
		if _, seen := vars[name]; !seen {
			vars[name] = value
		}
		if _, err := fmt.Fprintf(h, "var %s=%q\n", name, value); err != nil {
			return "", 0, nil, err
		}
	}
	env := os.Environ()
	slices.Sort(env)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_") {
			continue
		}
		if _, err := fmt.Fprintf(h, "env %q\n", kv); err != nil {
			return "", 0, nil, err
		}
	}
	for _, file := range []string{
		filepath.Join(c.s.GitDir(), "HEAD"),
		filepath.Join(c.s.CommonDir(), "refs", "remotes", "origin", "HEAD"),
		filepath.Join(c.s.CommonDir(), "shallow"),
	} {
		data, _ := os.ReadFile(file) //nolint:gosec // G304: fixed paths inside this repository's git directories.
		if _, err := fmt.Fprintf(h, "%s %q\n", file, data); err != nil {
			return "", 0, nil, err
		}
	}
	names := make([]string, 0, len(refs))
	for ref := range refs {
		if !strings.HasPrefix(ref, "refs/cc-notes-sync/") {
			names = append(names, ref)
		}
	}
	slices.Sort(names)
	for _, ref := range names {
		if _, err := fmt.Fprintf(h, "%s %s\n", ref, refs[ref]); err != nil {
			return "", 0, nil, err
		}
	}
	links := make([]string, 0, len(symbolic))
	for ref := range symbolic {
		links = append(links, ref)
	}
	slices.Sort(links)
	for _, ref := range links {
		if _, err := fmt.Fprintf(h, "%s -> %s\n", ref, symbolic[ref]); err != nil {
			return "", 0, nil, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), staleAfter, vars, nil
}

func relevantCacheName(gitDir, dir, p string, filter RelevantFilter, variant string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\n%s\n%s\n%+v\n%s", gitDir, dir, p, filter, variant))
	return hex.EncodeToString(sum[:])
}

func (e relevantCacheEntry) valid(now time.Time) bool {
	if e.Deadline != 0 && now.UnixNano() > e.Deadline {
		return false
	}
	for _, f := range e.Files {
		if stampOf(f.Path) != f {
			return false
		}
	}
	return true
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
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
