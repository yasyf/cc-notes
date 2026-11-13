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
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

type relevantCacheEntry struct {
	Key           string       `json:"key"`
	Built         int64        `json:"built"`
	Deadline      int64        `json:"deadline,omitempty"`
	Env           string       `json:"env"`
	Exe           string       `json:"exe"`
	Git           string       `json:"git"`
	Racy          bool         `json:"racy,omitempty"`
	RacyRefs      bool         `json:"racy_refs,omitempty"`
	Revalidate    bool         `json:"revalidate,omitempty"`
	Stamps        []fileStamp  `json:"stamps,omitempty"`
	Files         []fileStamp  `json:"files,omitempty"`
	Rendered      []string     `json:"rendered,omitempty"`
	LooseRefs     []string     `json:"loose_refs,omitempty"`
	HeadDeps      []fileStamp  `json:"head_deps,omitempty"`
	AnyHead       bool         `json:"any_head,omitempty"`
	ConfigSum     string       `json:"config_sum,omitempty"`
	LocalPushOnly bool         `json:"local_push_only,omitempty"`
	BaseFile      string       `json:"base_file,omitempty"`
	BaseRef       string       `json:"base_ref,omitempty"`
	Head          model.SHA    `json:"head,omitempty"`
	MergeBase     model.SHA    `json:"merge_base,omitempty"`
	Deps          relevantDeps `json:"-"`
	Output        []byte       `json:"-"`
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
				hit = c.restampUnscoredWrites(ctx, name, cached, p, filter, now)
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
		cached.LooseRefs = c.looseRefs(cached.Rendered)
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
	rendered := renderedRefs(entries)
	loose := c.looseRefs(rendered)
	after := stampsOf(paths)
	if !slices.Equal(before, after) || slices.ContainsFunc(after, func(f fileStamp) bool { return f.racyMtime(in.start) }) || in.noCache || !in.close() {
		return out, nil
	}
	entry := relevantCacheEntry{Key: in.key(), Files: after, Rendered: rendered, LooseRefs: loose, Output: out, MergeBase: in.mergeBase}
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

func (f fileStamp) coarse() bool {
	return f.ModTime%time.Second.Nanoseconds() == 0 && f.Ctime%time.Second.Nanoseconds() == 0
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
	return e.settled(now) && e.stampsValid() && e.filesValid() && e.headDepsValid()
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

// restampUnscoredWrites serves an entry whose only moved fingerprints are
// entity ref directories, each the same directory as before and holding none
// of the refs the entry renders or resolves a branch, base, or commit
// through, when every ref written there since the entry was built names an
// entity that scores nothing against p: one with no anchors, or with path
// and dir anchors nowhere at, above, or beside p. Such an entity cannot enter
// p's rendering, so a ledger's row writes, the bulk of a drive's churn, and
// edits to records anchored elsewhere leave it standing. An entry racy only
// in those directories qualifies too, since they are read afresh rather than
// trusted. The moved stamps, taken before the directories are read, persist
// with the check's start as the entry's build time once they are older than
// the racy window.
//
// A moved branch directory qualifies when every branch the entry resolves still
// has its own stamp, and a rewritten common config when only its [branch]
// sections changed: a busy repository moves both constantly, and neither a
// sibling branch nor another branch's tracking config reaches p's rendering. A
// moved default base qualifies when its merge base with HEAD is unchanged, the
// only thing rendering reads from it, at the cost of one git process. A moved
// local push include qualifies when it held and still holds only what cc-notes
// writes there, which only pushes read.
func (c *Client) restampUnscoredWrites(ctx context.Context, name string, e relevantCacheEntry, p string, filter RelevantFilter, now time.Time) bool {
	if (e.Racy && !e.RacyRefs) || !e.built(now) || !e.filesValid() {
		return false
	}
	roots := c.entityRootDirs()
	resolved := slices.Concat(e.Deps.Branches, e.Deps.Commits, []string{filter.Branch, filter.Base})
	built := time.Unix(0, e.Built)
	stamps := slices.Clone(e.Stamps)
	var moved []int
	headsMoved, configMoved, baseMoved := false, false, false
	config := joinPath(c.s.CommonDir(), "config")
	for i, s := range e.Stamps {
		if stamps[i] = restamp(s); stamps[i] == s && (!e.Racy || !s.racy(built)) {
			continue
		}
		if s.Path == config && !stamps[i].Missing {
			configMoved = true
			continue
		}
		if s.Path == e.BaseFile && !stamps[i].Missing {
			baseMoved = true
			continue
		}
		if s.Path == c.localPushFile() && e.LocalPushOnly && localPushOnly(s.Path) {
			continue
		}
		root, ok := roots[s.Path]
		heads := c.branchDir(s) && (!e.AnyHead || s.Path == c.headsDir())
		if (!ok && !heads) || s.Link || s.Ident || stamps[i].Missing || stamps[i].Inode != s.Inode || stamps[i].Mode != s.Mode {
			return false
		}
		if heads {
			headsMoved = true
			continue
		}
		if slices.ContainsFunc(e.LooseRefs, func(ref string) bool { return strings.HasPrefix(ref, root) && !c.looseRef(ref) }) ||
			slices.ContainsFunc(resolved, func(rev string) bool { return strings.Contains(rev, strings.TrimPrefix(root, "refs/")) }) {
			return false
		}
		moved = append(moved, i)
	}
	cutoff := e.Built - relevantRacyWindow.Nanoseconds()
	if !c.resolvedBranchesHeld(e, headsMoved, built, cutoff) {
		return false
	}
	if configMoved {
		if sum := configSumWithoutBranches(config); sum == "" || sum != e.ConfigSum {
			return false
		}
	}
	if baseMoved && !c.mergeBaseHeld(ctx, e) {
		return false
	}
	for _, i := range moved {
		if !c.unscoredSince(ctx, e.Stamps[i].Path, roots[e.Stamps[i].Path], p, e.Rendered, cutoff) {
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

func (c *Client) headsDir() string {
	return joinPath(c.s.RecordsCommonDir(), filepath.FromSlash("refs/heads"))
}

func (c *Client) branchDir(s fileStamp) bool {
	if !s.Mode.IsDir() {
		return false
	}
	for _, dir := range []string{c.headsDir(), joinPath(c.s.RecordsCommonDir(), filepath.FromSlash("refs/remotes"))} {
		if s.Path == dir || strings.HasPrefix(s.Path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (c *Client) localPushFile() string {
	return joinPath(c.s.RecordsCommonDir(), filepath.FromSlash(store.LocalPushInclude))
}

func localPushOnly(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // G304: this repository's own local push include.
	return err == nil && store.IsLocalPushConfig(data)
}

func (c *Client) mergeBaseHeld(ctx context.Context, e relevantCacheEntry) bool {
	if e.MergeBase == "" {
		return false
	}
	data, err := os.ReadFile(e.BaseFile) //nolint:gosec // G304: a ref file this process recorded in its own cache entry.
	if err != nil {
		return false
	}
	if _, symbolic := symrefTarget(data); symbolic {
		return false
	}
	mergeBase, err := c.s.Git.MergeBase(ctx, e.BaseRef, string(e.Head))
	return err == nil && mergeBase == e.MergeBase
}

func (c *Client) resolvedBranchesHeld(e relevantCacheEntry, headsMoved bool, built time.Time, cutoff int64) bool {
	if e.AnyHead {
		return !headsMoved || onlyLiveRefSince(c.headsDir(), cutoff)
	}
	racy := e.Racy && slices.ContainsFunc(e.HeadDeps, func(h fileStamp) bool { return h.racy(built) })
	return !racy && e.headDepsValid()
}

// onlyLiveRefSince reports whether the source projection's live ref, which every
// managed write moves, is the one entry of the branch directory written at or
// after cutoff (nanoseconds). A branch removed since is watched by its own stamp.
func onlyLiveRefSince(dir string, cutoff int64) bool {
	live := strings.TrimPrefix(sourceindex.LiveRef, "refs/heads/")
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
		if ctime, _ := gitobj.StatIdentity(info); entry.Name() != live && (ctime >= cutoff || info.ModTime().UnixNano() >= cutoff) {
			return false
		}
	}
	return true
}

// unscoredSince reports whether every loose ref in dir, the directory of
// root, written at or after cutoff (nanoseconds) holds an entity that scores
// nothing against p and is not one the entry renders. A ref removed since is
// no rendered entry's, the caller having checked those, so it changes nothing.
func (c *Client) unscoredSince(ctx context.Context, dir, root, p string, rendered []string, cutoff int64) bool {
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
		if !info.Mode().IsRegular() || strings.HasSuffix(entry.Name(), ".lock") || slices.Contains(rendered, root+entry.Name()) {
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
		if e, ok := snapshotEntry(snapshot); !ok || !scoresNothingAt(e.anchors(), p) {
			return false
		}
	}
	return true
}

// scoresNothingAt reports whether anchors earn no score for p in any
// repository state: every anchor is a path or dir anchor, and none is at p,
// above it, or beside it. Branch and commit anchors score from history, so
// an entity carrying one never qualifies.
func scoresNothingAt(anchors []model.Anchor, p string) bool {
	for _, a := range anchors {
		if a.Kind != model.AnchorPath && a.Kind != model.AnchorDir {
			return false
		}
	}
	return !hasAnchorIn(anchors, model.AnchorPath, p) && deepestDirAnchor(anchors, p) == "" && len(siblingAnchors(anchors, p)) == 0
}

// looseRefs lists the rendered refs that are loose files, the ones whose
// deletion leaves packed-refs untouched. Callers take it before the inputs'
// final validation, which a deletion racing it fails.
func (c *Client) looseRefs(rendered []string) []string {
	var loose []string
	for _, ref := range rendered {
		if c.looseRef(ref) {
			loose = append(loose, ref)
		}
	}
	return loose
}

func renderedRefs(entries []RelevantEntry) []string {
	rendered := make([]string, len(entries))
	for i, e := range entries {
		rendered[i] = refs.For(e.Kind, e.id())
	}
	return rendered
}

func (c *Client) looseRef(ref string) bool {
	info, err := os.Lstat(joinPath(c.s.RecordsCommonDir(), filepath.FromSlash(ref)))
	return err == nil && info.Mode().IsRegular()
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

func (e relevantCacheEntry) headDepsValid() bool {
	return !slices.ContainsFunc(e.HeadDeps, func(h fileStamp) bool { return lstampOf(h.Path) != h })
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
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{Path: path, Ident: true, Missing: true}
	}
	_, inode := gitobj.FileID(info)
	return fileStamp{Path: path, Ident: true, Inode: inode, Mode: info.Mode().Type()}
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	return stampFrom(path, info, err, false)
}

func lstampOf(path string) fileStamp {
	info, err := os.Lstat(path)
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
