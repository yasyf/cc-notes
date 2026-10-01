package notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type relevantCacheEntry struct {
	Key        string       `json:"key"`
	Built      int64        `json:"built"`
	Deadline   int64        `json:"deadline,omitempty"`
	Env        string       `json:"env"`
	Exe        string       `json:"exe"`
	Git        string       `json:"git"`
	Racy       bool         `json:"racy,omitempty"`
	Revalidate bool         `json:"revalidate,omitempty"`
	Stamps     []fileStamp  `json:"stamps,omitempty"`
	Files      []fileStamp  `json:"files,omitempty"`
	Deps       relevantDeps `json:"deps"`
	Output     string       `json:"output"`
}

var relevantRefRoots = []string{
	refs.Root(model.KindNote),
	refs.Root(model.KindDoc),
	refs.Root(model.KindAnswer),
	refs.Root(model.KindLedger),
	refs.Root(model.KindLog),
	refs.Root(model.KindRunbook),
	refs.Root(model.KindInvestigation),
	refs.Root(model.KindPlan),
}

type fileStamp struct {
	Path    string      `json:"path"`
	Missing bool        `json:"missing,omitempty"`
	Link    bool        `json:"link,omitempty"`
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
// A warm hit re-stats the captured fingerprints and runs no git; a moved
// fingerprint rebuilds the content key through one ref enumeration, and only
// a changed key scores again.
func (c *Client) RelevantCached(ctx context.Context, target string, filter RelevantFilter, variant string, render func([]RelevantEntry) ([]byte, error)) ([]byte, error) {
	p, err := c.relevantPath(ctx, target)
	if err != nil {
		return nil, err
	}
	name := relevantCacheName(c.s.GitDir(), c.s.Git.Dir, p, filter, variant)
	now := time.Now()
	var cached relevantCacheEntry
	var cachedOK bool
	if data, ok := c.s.ReadRelevantCache(name); ok {
		cachedOK = json.Unmarshal(data, &cached) == nil
	}
	// Lock-and-rename publishes (git, libgit2, gix, JGit, cc-notes) move a
	// stamp; a foreign in-place rewrite of an existing loose ref under a
	// directory stamp (go-git setRef) is not covered.
	if cachedOK && cached.hit(now) {
		return []byte(cached.Output), nil
	}
	var deps relevantDeps
	if cachedOK {
		deps = cached.Deps
	}
	c.s.EnsureCaches()
	in, err := c.relevantInputs(ctx, p, filter, variant, deps)
	if err != nil {
		return nil, err
	}
	if cachedOK && cached.Key == in.key() && cached.clockValid(in.start) && cached.filesValid() {
		if in.close() {
			in.fill(&cached)
			c.writeRelevantCache(name, cached)
		}
		return []byte(cached.Output), nil
	}
	entries, err := c.relevantScored(ctx, in, filter)
	if err != nil {
		return nil, err
	}
	var paths, anchors []string
	if filter.Worktree {
		if paths, anchors, err = c.driftInputs(ctx, entries, in.vars); err != nil {
			return nil, err
		}
	}
	before := stampsOf(paths)
	if err := in.auditWorktree(ctx, anchors); err != nil {
		return nil, err
	}
	if err := c.relevantVerdicts(ctx, entries, in, filter.Worktree); err != nil {
		return nil, err
	}
	out, err := render(entries)
	if err != nil {
		return nil, err
	}
	after := stampsOf(paths)
	if !slices.Equal(before, after) || slices.ContainsFunc(after, func(f fileStamp) bool { return f.racyMtime(in.start) }) || in.noCache || !in.close() {
		return out, nil
	}
	entry := relevantCacheEntry{Key: in.key(), Files: after, Output: string(out)}
	in.fill(&entry)
	for _, e := range entries {
		if fe, ok := freshOf(e); ok && e.Verdict == "" && fe.StaleAt == 0 && fe.VerifiedAt != 0 {
			deadline := time.Unix(fe.VerifiedAt, 0).Add(in.staleAfter).UnixNano()
			if entry.Deadline == 0 || deadline < entry.Deadline {
				entry.Deadline = deadline
			}
		}
	}
	c.writeRelevantCache(name, entry)
	return out, nil
}

func (c *Client) writeRelevantCache(name string, entry relevantCacheEntry) {
	if data, err := json.Marshal(entry); err == nil {
		c.s.WriteRelevantCache(name, data)
	}
}

func (c *Client) driftInputs(ctx context.Context, entries []RelevantEntry, vars map[string]string) (paths, anchors []string, err error) {
	add := func(p string) {
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	root, err := c.s.Root(ctx)
	if err != nil {
		return nil, nil, err
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
			if !slices.Contains(anchors, file) {
				anchors = append(anchors, file)
			}
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
		return nil, nil, nil
	}
	add(filepath.Join(c.s.CommonDir(), "info", "attributes"))
	for _, v := range []string{"GIT_ATTR_GLOBAL", "GIT_ATTR_SYSTEM"} {
		if p := vars[v]; p != "" {
			add(p)
		}
	}
	return paths, anchors, nil
}

func stampsOf(paths []string) []fileStamp {
	stamps := make([]fileStamp, len(paths))
	for i, p := range paths {
		stamps[i] = stampOf(p)
	}
	return stamps
}

func (f fileStamp) racyMtime(start time.Time) bool {
	return !f.Missing && time.Unix(0, f.ModTime).After(start.Add(-relevantRacyWindow))
}

func (f fileStamp) racy(start time.Time) bool {
	edge := start.Add(-relevantRacyWindow)
	return !f.Missing && (time.Unix(0, f.ModTime).After(edge) || time.Unix(0, f.Ctime).After(edge))
}

func relevantCacheName(gitDir, dir, p string, filter RelevantFilter, variant string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\n%s\n%s\n%+v\n%s", gitDir, dir, p, filter, variant))
	return hex.EncodeToString(sum[:])
}

func (e relevantCacheEntry) hit(now time.Time) bool {
	if e.Racy || e.Revalidate || !e.clockValid(now) || e.Env != relevantEnv() {
		return false
	}
	if exe, err := os.Executable(); err != nil || exe != e.Exe {
		return false
	}
	if git, err := exec.LookPath("git"); err != nil || git != e.Git {
		return false
	}
	return e.stampsValid() && e.filesValid()
}

func (e relevantCacheEntry) clockValid(now time.Time) bool {
	ns := now.UnixNano()
	return ns >= e.Built && (e.Deadline == 0 || ns <= e.Deadline)
}

func (e relevantCacheEntry) stampsValid() bool {
	for _, s := range e.Stamps {
		if restamp(s) != s {
			return false
		}
	}
	return true
}

func (e relevantCacheEntry) filesValid() bool {
	for _, f := range e.Files {
		if stampOf(f.Path) != f {
			return false
		}
	}
	return true
}

func restamp(s fileStamp) fileStamp {
	if s.Link {
		return lstampOf(s.Path)
	}
	return stampOf(s.Path)
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path) //nolint:gosec // G703: stats only paths this process recorded in its own cache entry.
	return stampFrom(path, info, err, false)
}

func lstampOf(path string) fileStamp {
	info, err := os.Lstat(path) //nolint:gosec // G703: stats only paths this process recorded in its own cache entry.
	return stampFrom(path, info, err, true)
}

func stampFrom(path string, info os.FileInfo, err error, link bool) fileStamp {
	if err != nil {
		return fileStamp{Path: path, Missing: true, Link: link}
	}
	ctime, inode := gitobj.StatIdentity(info)
	s := fileStamp{Path: path, Link: link, Inode: inode, Mode: info.Mode()}
	// A device node has no content to fingerprint, and /dev/null's mtime and
	// ctime move on every write to it.
	if info.Mode()&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 {
		return s
	}
	s.Size, s.ModTime, s.Ctime = info.Size(), info.ModTime().UnixNano(), ctime
	return s
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
