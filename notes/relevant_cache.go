package notes

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
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
	RacyRefs   bool         `json:"racy_refs,omitempty"`
	Revalidate bool         `json:"revalidate,omitempty"`
	Stamps     []fileStamp  `json:"stamps,omitempty"`
	Files      []fileStamp  `json:"files,omitempty"`
	Rendered   []string     `json:"rendered,omitempty"`
	Deps       relevantDeps `json:"-"`
	Output     []byte       `json:"-"`
}

type relevantCacheHeader struct {
	relevantCacheEntry
	OutputLen int `json:"output_len"`
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
	Ident   bool        `json:"ident,omitempty"`
	Size    int64       `json:"size,omitempty"`
	ModTime int64       `json:"mtime,omitempty"`
	Ctime   int64       `json:"ctime,omitempty"`
	Dev     uint64      `json:"dev,omitempty"`
	Inode   uint64      `json:"inode,omitempty"`
	Mode    os.FileMode `json:"mode,omitempty"`
}

// RelevantCached renders the Relevant result for target through render,
// serving it from the repository's relevance cache when nothing the result
// depends on has changed. variant names everything render bakes into its
// output (format, limit), so two renderings never share an entry.
//
// A warm hit re-stats the fingerprints and runs no git; on a bound store it
// is also the backend recheck. A moved fingerprint rebuilds the key, and
// only a changed key scores again.
func (c *Client) RelevantCached(ctx context.Context, target string, filter RelevantFilter, variant string, render func([]RelevantEntry) ([]byte, error)) ([]byte, error) {
	p, err := c.relevantPath(ctx, target)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	name := c.relevantName(p, filter, variant, exe)
	now := time.Now()
	var cached relevantCacheEntry
	var cachedOK bool
	if !relevantRouted() {
		if f, ok := c.s.OpenRelevantCache(name); ok {
			var hit bool
			// Lock-and-rename publishes (git, libgit2, gix, JGit, cc-notes) move a
			// stamp; a foreign in-place rewrite of an existing loose ref under a
			// directory stamp (go-git setRef) is not covered.
			cached, hit, cachedOK = readRelevantCacheEntry(f, now)
			if !hit && cachedOK {
				hit = c.restampUnanchoredWrites(ctx, name, cached, filter, now)
			}
			if hit {
				if _, bound := c.s.Binding(); bound {
					if err := c.s.CheckRecords(); err != nil {
						return nil, err
					}
				}
				return cached.Output, nil
			}
		}
	}
	var deps relevantDeps
	if cachedOK {
		deps = cached.Deps
	}
	in, err := c.relevantInputs(ctx, p, filter, variant, deps)
	if err != nil {
		return nil, err
	}
	if cachedOK && cached.Key == in.key() && cached.clockValid(in.start) && cached.filesValid() {
		if in.close() {
			in.fill(&cached)
			c.writeRelevantCache(name, cached)
		}
		return cached.Output, nil
	}
	entries, err := c.relevantScored(ctx, in, filter)
	if err != nil {
		return nil, err
	}
	var paths, anchors []string
	if filter.Worktree {
		if paths, anchors, err = c.driftInputs(ctx, entries, in); err != nil {
			return nil, err
		}
	}
	before := in.resolveFiles(paths)
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
	entry := relevantCacheEntry{Key: in.key(), Files: after, Rendered: renderedRefs(entries), Output: out}
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
	if data, err := entry.encode(); err == nil {
		c.s.WriteRelevantCache(name, data)
	}
}

// encode writes the JSON header, a newline, exactly output_len bytes of raw
// output, then the deps JSON, so a hit decodes the header alone and reads the
// output by length, never the deps. json.Marshal escapes every newline, so
// the first one ends the header.
func (e relevantCacheEntry) encode() ([]byte, error) {
	header, err := json.Marshal(relevantCacheHeader{relevantCacheEntry: e, OutputLen: len(e.Output)})
	if err != nil {
		return nil, err
	}
	deps, err := json.Marshal(e.Deps)
	if err != nil {
		return nil, err
	}
	return slices.Concat(header, []byte{'\n'}, e.Output, deps), nil
}

func readRelevantCacheEntry(f *store.CacheReader, now time.Time) (e relevantCacheEntry, hit, ok bool) {
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	h, n, ok := readRelevantCacheHeader(r)
	if !ok || !h.readOutput(r, f.Size()-int64(n)) {
		return relevantCacheEntry{}, false, false
	}
	if h.hit(now) {
		return h.relevantCacheEntry, true, true
	}
	if !h.readDeps(r) {
		return relevantCacheEntry{}, false, false
	}
	return h.relevantCacheEntry, false, true
}

func readRelevantCacheHeader(r *bufio.Reader) (h relevantCacheHeader, n int, ok bool) {
	line, err := r.ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &h) != nil {
		return relevantCacheHeader{}, 0, false
	}
	return h, len(line), true
}

func (h *relevantCacheHeader) readOutput(r io.Reader, remaining int64) bool {
	if h.OutputLen < 0 || int64(h.OutputLen) > remaining {
		return false
	}
	h.Output = make([]byte, h.OutputLen)
	_, err := io.ReadFull(r, h.Output)
	return err == nil
}

func (h *relevantCacheHeader) readDeps(r io.Reader) bool {
	data, err := io.ReadAll(r)
	return err == nil && json.Unmarshal(data, &h.Deps) == nil
}

func (c *Client) driftInputs(ctx context.Context, entries []RelevantEntry, in *relevantInputs) (paths, anchors []string, err error) {
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
		p := in.vars[v]
		if p == "" {
			continue
		}
		resolved, ok := in.gitPath(ctx, p)
		if !ok {
			in.noCache = true
			continue
		}
		add(resolved)
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

func (c *Client) relevantName(p string, filter RelevantFilter, variant, exe string) string {
	binding := ""
	if b, bound := c.s.Binding(); bound {
		binding = b.String()
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\n%s\n%s\n%s\n%+v\n%d %s\n%s", c.s.GitDir(), c.s.Git.Dir, binding, p, filter, len(exe), exe, variant))
	return hex.EncodeToString(sum[:])
}

func (e relevantCacheEntry) hit(now time.Time) bool {
	return e.settled(now) && e.stampsValid() && e.filesValid()
}

// settled reports whether everything but the fingerprints still holds: the
// entry is neither racy nor marked for revalidation, its clock window is
// open, and it was built under this environment, executable, and git.
func (e relevantCacheEntry) settled(now time.Time) bool {
	return !e.Racy && e.built(now)
}

func (e relevantCacheEntry) built(now time.Time) bool {
	if e.Revalidate || !e.clockValid(now) || e.Env != relevantEnv() {
		return false
	}
	if exe, err := os.Executable(); err != nil || exe != e.Exe {
		return false
	}
	git, err := exec.LookPath("git")
	return err == nil && git == e.Git
}

// restampUnanchoredWrites serves an entry whose only moved fingerprints are
// entity ref directories, each the same directory as before and holding none
// of the refs the entry renders or resolves a branch, base, or commit
// through, when every ref written there since the entry was built names an
// entity with no anchors.
// Such an entity scores nothing against any path, so a ledger's row writes,
// the bulk of a drive's churn, leave every rendering standing. An entry racy
// only in those directories qualifies too, since they are read afresh rather
// than trusted. The moved stamps, taken before the directories are read,
// persist with the check's start as the entry's build time once they are
// older than the racy window.
func (c *Client) restampUnanchoredWrites(ctx context.Context, name string, e relevantCacheEntry, filter RelevantFilter, now time.Time) bool {
	if (e.Racy && !e.RacyRefs) || !e.built(now) || !e.filesValid() {
		return false
	}
	roots := c.entityRootDirs()
	resolved := slices.Concat(e.Deps.Branches, e.Deps.Commits, []string{filter.Branch, filter.Base})
	built := time.Unix(0, e.Built)
	stamps := slices.Clone(e.Stamps)
	var moved []int
	for i, s := range e.Stamps {
		if stamps[i] = restamp(s); stamps[i] == s && !(e.Racy && s.racy(built)) {
			continue
		}
		root, ok := roots[s.Path]
		if !ok || s.Link || s.Ident || stamps[i].Missing || stamps[i].Inode != s.Inode || stamps[i].Mode != s.Mode {
			return false
		}
		if slices.ContainsFunc(e.Rendered, func(ref string) bool { return strings.HasPrefix(ref, root) }) ||
			slices.ContainsFunc(resolved, func(rev string) bool { return strings.Contains(rev, strings.TrimPrefix(root, "refs/")) }) {
			return false
		}
		moved = append(moved, i)
	}
	cutoff := e.Built - relevantRacyWindow.Nanoseconds()
	for _, i := range moved {
		if !c.unanchoredSince(ctx, e.Stamps[i].Path, cutoff) {
			return false
		}
	}
	if !slices.ContainsFunc(stamps, func(s fileStamp) bool { return s.racy(now) }) {
		e.Built, e.Stamps, e.Racy = now.UnixNano(), stamps, false
		c.writeRelevantCache(name, e)
	}
	return true
}

// entityRootDirs maps each relevance kind's loose ref directory to its root.
func (c *Client) entityRootDirs() map[string]string {
	dirs := make(map[string]string, len(relevantRefRoots))
	for _, root := range relevantRefRoots {
		dirs[joinPath(c.s.RecordsCommonDir(), filepath.FromSlash(strings.TrimSuffix(root, "/")))] = root
	}
	return dirs
}

// unanchoredSince reports whether every loose ref in dir written at or after
// cutoff (nanoseconds) holds an entity with no anchors. A ref removed since is
// no rendered entry's, so it changes nothing.
func (c *Client) unanchoredSince(ctx context.Context, dir string, cutoff int64) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false
		}
		if ctime, _ := gitobj.StatIdentity(info); ctime < cutoff && info.ModTime().UnixNano() < cutoff {
			continue
		}
		if !info.Mode().IsRegular() || strings.HasSuffix(entry.Name(), ".lock") {
			return false
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // G304: a loose ref under this store's own ref directory.
		tip, found := strings.CutSuffix(string(data), "\n")
		if err != nil || !found || !plumbing.IsHash(tip) {
			return false
		}
		snapshot, err := c.s.LoadAt(ctx, model.SHA(tip))
		if err != nil {
			return false
		}
		if e, ok := snapshotEntry(snapshot); !ok || len(e.anchors()) > 0 {
			return false
		}
	}
	return true
}

func renderedRefs(entries []RelevantEntry) []string {
	rendered := make([]string, len(entries))
	for i, e := range entries {
		rendered[i] = refs.For(e.Kind, e.id())
	}
	return rendered
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
	switch {
	case s.Ident:
		return identStampOf(s.Path)
	case s.Link:
		return lstampOf(s.Path)
	}
	return stampOf(s.Path)
}

// identStampOf fingerprints a directory by inode alone: the backend common
// directory's mtime and ctime move on every checkout, commit, fetch, and
// config write there, none of which changes a bound context's relevance, and
// its device number moves when macOS renumbers volumes across a reboot.
func identStampOf(path string) fileStamp {
	info, err := os.Stat(path) //nolint:gosec // G703: stats only the backend directory this process recorded in its own cache entry.
	if err != nil {
		return fileStamp{Path: path, Ident: true, Missing: true}
	}
	_, inode := gitobj.FileID(info)
	return fileStamp{Path: path, Ident: true, Inode: inode, Mode: info.Mode().Type()}
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
