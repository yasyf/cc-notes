//go:build fuse

package fusefs

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/fusekit"
	"github.com/yasyf/fusekit/content"
)

// ServeContent runs cc-notes' content server on socket until ctx is canceled:
// the store→synthetic-tree renderer exposed as a content.Tree/HandleTree the
// shared fusekit holder consumes in ContentModeTree over the bridge. One server
// serves every repo, keyed by the bridge domain (the repo root). A
// LaunchAgent-supervised process (cc-notes contentd) keeps it up so the shared
// holder can re-dial the socket when it replays a mount at serve time. The
// BridgeServer is SingleEntrant, so exactly one instance ever binds the socket.
func ServeContent(ctx context.Context, socket string) error {
	srv := &content.BridgeServer{
		Socket: socket,
		Source: newContentSource(),
		// cc-notes' APP version, never fusekit's: a consumer comparing the
		// holder's wire version to fusekit's own would loop forever.
		Version: version.String(),
	}
	return srv.Run(ctx)
}

// contentSource routes every bridge op to the repo the domain names. domain is
// the repo root: viewFor lazily opens the store and builds the renderer there,
// so one long-lived server serves N repos.
type contentSource struct {
	mu    sync.Mutex
	views map[string]*repoView
}

func newContentSource() *contentSource {
	return &contentSource{views: map[string]*repoView{}}
}

var (
	_ content.Source       = (*contentSource)(nil)
	_ content.Tree         = (*contentSource)(nil)
	_ content.WritableTree = (*contentSource)(nil)
	_ content.HandleTree   = (*contentSource)(nil)
)

// repoView is one repo's renderer plus its per-open handle-token bookkeeping.
// The *FS owns the render cache, edit buffers, and commit path (its own mutex);
// tokens maps a HandleTree token to the FS file handle and the path it is bound
// to, so a token used against a different name fails ClassInvalid and
// ReleaseAllHandles can drop the whole domain on a holder-generation change.
type repoView struct {
	fs *FS

	// commitGate serializes committing ops against a generation-change discard:
	// every commit (FlushHandle, the ReleaseHandle backstop) holds the READ side
	// across its whole resolve→snapshot→append→finalize, and releaseAll takes the
	// WRITE side FIRST — blocking new commits and draining in-flight ones — before
	// discarding, so no commit's store append can land concurrently with or after
	// the discard and make a torn partial write canonical (the ReleaseAll race).
	commitGate sync.RWMutex

	// gen is the holder-generation counter releaseAll bumps (under commitGate's
	// write side). OpenHandle snapshots it before taking the gate and refuses any
	// open whose snapshot went stale — the open straddled a generation teardown.
	gen atomic.Uint64

	mu     sync.Mutex
	tokens map[string]tokenState
}

type tokenState struct {
	fh   uint64
	path string
}

// viewFor returns domain's renderer, opening the store at the repo root the
// first time. A domain that is not a readable repo fails loudly — the holder
// classifies the bridge error as content-unavailable and fails the mount rather
// than serving an empty tree.
func (s *contentSource) viewFor(domain string) (*repoView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.views[domain]; ok {
		return v, nil
	}
	st, err := store.Open(domain)
	if err != nil {
		return nil, &treeErr{class: content.ClassTransient, msg: fmt.Sprintf("open store at %s: %v", domain, err)}
	}
	v := &repoView{fs: newFS(context.Background(), st), tokens: map[string]tokenState{}}
	s.views[domain] = v
	return v, nil
}

// existingView returns domain's view without opening one — the ReleaseAll sweep
// has nothing to drop for a domain never served.
func (s *contentSource) existingView(domain string) *repoView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.views[domain]
}

// entryFor renders a resolved fuse stat plus cc-notes' cache-defeat metadata
// into a content.Entry. Version is the entity's chain-tip SHA (seed), and st
// plus seed MUST be one atomic snapshot (rv.fs.statSeed) so size/mtime never
// pair with a different version's SHA. The holder's tree view bumps the served
// mtime monotonically whenever Version changes, so a same-second commit still
// forces the NFS client to revalidate. Mtime folds VersionNsec into the
// second-granular timestamp exactly as the in-process cache-defeat decorator
// does; a versionless node (dir, scratch, pending) keeps its raw stat time. Ino
// is the FS's stable identity key — the holder mints its own fileid keyed on it,
// never serving it raw.
func (rv *repoView) entryFor(name string, st *fuse.Stat_t, seed string) content.Entry {
	kind := content.EntrySynth
	switch st.Mode & fuse.S_IFMT {
	case fuse.S_IFDIR:
		kind = content.EntryDir
	case fuse.S_IFLNK:
		kind = content.EntrySymlink
	}
	mtime := st.Mtim.Sec*1_000_000_000 + st.Mtim.Nsec
	if seed != "" {
		mtime = st.Mtim.Sec*1_000_000_000 + fusekit.VersionNsec(seed)
	}
	return content.Entry{
		Name:    path.Base(name),
		Kind:    kind,
		Version: seed,
		Size:    st.Size,
		Mtime:   mtime,
		Birth:   st.Birthtim.Sec*1_000_000_000 + st.Birthtim.Nsec,
		Ino:     st.Ino,
	}
}

// --- content.Source (unused in tree mode, required by the interface) ---

func (s *contentSource) Manifest(domain string) ([]content.Entry, error) {
	return s.List(domain, "/")
}

func (s *contentSource) ReadSynth(domain, name string) ([]byte, error) {
	var out []byte
	for {
		chunk, err := s.ReadAt(domain, name, int64(len(out)), 1<<20)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if len(chunk) < 1<<20 {
			return out, nil
		}
	}
}

// WriteThrough overwrites name with data through the commit-on-flush handle
// path, so an out-of-band source write commits with the same parse+diff+append
// semantics a mount edit does. Tree mode never sends it (writes route through
// the handle ops); it exists to satisfy content.Source honestly.
func (s *contentSource) WriteThrough(domain, name string, data []byte) error {
	tok, _, err := s.OpenHandle(domain, name)
	if err != nil {
		return err
	}
	defer func() { _ = s.ReleaseHandle(domain, name, tok) }()
	if err := s.TruncateHandle(domain, name, tok, int64(len(data))); err != nil {
		return err
	}
	if err := s.WriteAtHandle(domain, name, tok, 0, data); err != nil {
		return err
	}
	return s.FlushHandle(domain, name, tok)
}

func (s *contentSource) Classify(string) content.EntryKind { return content.EntrySynth }

// --- content.Tree ---

func (s *contentSource) Stat(domain, name string) (content.Entry, error) {
	rv, err := s.viewFor(domain)
	if err != nil {
		return content.Entry{}, err
	}
	st, seed, rc := rv.fs.statSeed(name, invalidFh)
	if rc != 0 {
		return content.Entry{}, errnoClass(rc)
	}
	return rv.entryFor(name, &st, seed), nil
}

func (s *contentSource) List(domain, name string) ([]content.Entry, error) {
	rv, err := s.viewFor(domain)
	if err != nil {
		return nil, err
	}
	var names []string
	fill := func(child string, _ *fuse.Stat_t, _ int64) bool {
		if child != "." && child != ".." {
			names = append(names, child)
		}
		return true
	}
	if rc := rv.fs.Readdir(name, fill, 0, invalidFh); rc != 0 {
		return nil, errnoClass(rc)
	}
	entries := make([]content.Entry, 0, len(names))
	for _, child := range names {
		cp := path.Join(name, child)
		st, seed, rc := rv.fs.statSeed(cp, invalidFh)
		switch {
		case rc == 0:
			entries = append(entries, rv.entryFor(cp, &st, seed))
		case rc == -fuse.ENOENT:
			continue // vanished between readdir and stat — legitimately gone
		default:
			// A transient store failure must not silently drop a live child
			// (a stale listing deletes a note from the holder's tree).
			return nil, errnoClass(rc)
		}
	}
	return entries, nil
}

func (s *contentSource) ReadAt(domain, name string, ofst int64, size int) ([]byte, error) {
	rv, err := s.viewFor(domain)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	rc := rv.fs.Read(name, buf, ofst, invalidFh)
	if rc < 0 {
		return nil, errnoClass(rc)
	}
	return buf[:rc], nil
}

func (s *contentSource) Readlink(domain, name string) (string, error) {
	rv, err := s.viewFor(domain)
	if err != nil {
		return "", err
	}
	rc, target := rv.fs.Readlink(name)
	if rc != 0 {
		return "", errnoClass(rc)
	}
	return target, nil
}

// --- content.WritableTree (path-wise; the holder uses the handle ops instead) ---

func (s *contentSource) Create(domain, name string) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	rc, fh := rv.fs.Create(name, 0, 0o644)
	if rc != 0 {
		return errnoClass(rc)
	}
	rv.fs.Release(name, fh) // the scratch file persists; drop the transient handle
	return nil
}

func (s *contentSource) WriteAt(domain, name string, ofst int64, data []byte) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	if rc := rv.fs.Write(name, data, ofst, invalidFh); rc < 0 {
		return errnoClass(rc)
	}
	return nil
}

func (s *contentSource) Truncate(domain, name string, size int64) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	if rc := rv.fs.Truncate(name, size, invalidFh); rc != 0 {
		return errnoClass(rc)
	}
	return nil
}

func (s *contentSource) Unlink(domain, name string) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	if rc := rv.fs.Unlink(name); rc != 0 {
		return errnoClass(rc)
	}
	return nil
}

func (s *contentSource) Rename(domain, oldName, newName string) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	if rc := rv.fs.Rename(oldName, newName); rc != 0 {
		return errnoClass(rc)
	}
	return nil
}

func (s *contentSource) Mkdir(domain, name string) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	if rc := rv.fs.Mkdir(name, 0o755); rc != 0 {
		return errnoClass(rc)
	}
	return nil
}

// --- content.HandleTree (per-open snapshot + edit buffer; commit on flush) ---

func (s *contentSource) OpenHandle(domain, name string) (string, content.Entry, error) {
	rv, err := s.viewFor(domain)
	if err != nil {
		return "", content.Entry{}, err
	}
	// Hold the commit gate's read side across create→stat→mint so a releaseAll
	// (holder generation change) can't sweep between minting the FS handle and its
	// token — which would strand an untracked handle a later gated Flush could
	// commit past the discard. If the generation advanced while this open was
	// mid-flight it straddled a teardown: refuse transient so the reopen lands on
	// the new generation instead of opening against the closing one.
	gen := rv.gen.Load()
	rv.commitGate.RLock()
	defer rv.commitGate.RUnlock()
	if rv.gen.Load() != gen {
		return "", content.Entry{}, &treeErr{class: content.ClassTransient, msg: "OpenHandle raced a generation teardown; retry"}
	}
	rc, fh := rv.fs.Open(name, 0)
	if rc != 0 {
		return "", content.Entry{}, errnoClass(rc)
	}
	st, seed, src := rv.fs.statSeed(name, fh)
	if src != 0 {
		rv.fs.Release(name, fh)
		return "", content.Entry{}, errnoClass(src)
	}
	return rv.mint(fh, name), rv.entryFor(name, &st, seed), nil
}

func (s *contentSource) ReadAtHandle(domain, name, token string, ofst int64, size int) ([]byte, error) {
	rv, err := s.viewFor(domain)
	if err != nil {
		return nil, err
	}
	fh, err := rv.resolve(token, name)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	rc := rv.fs.Read(name, buf, ofst, fh)
	if rc < 0 {
		return nil, errnoClass(rc)
	}
	return buf[:rc], nil
}

func (s *contentSource) WriteAtHandle(domain, name, token string, ofst int64, data []byte) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	fh, err := rv.resolve(token, name)
	if err != nil {
		return err
	}
	if rc := rv.fs.Write(name, data, ofst, fh); rc < 0 {
		return errnoClass(rc)
	}
	return nil
}

func (s *contentSource) TruncateHandle(domain, name, token string, size int64) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	fh, err := rv.resolve(token, name)
	if err != nil {
		return err
	}
	if rc := rv.fs.Truncate(name, size, fh); rc != 0 {
		return errnoClass(rc)
	}
	return nil
}

// FlushHandle commits the handle's dirty buffer and returns the commit verdict —
// notesCommit, the same hook the in-process host fires on Flush and Fsync. A
// rejected save (ClassInvalid parse failure, ClassPerm immutable edit) reaches
// the writer here, at its fsync/close boundary; a transient failure leaves the
// buffer dirty so ReleaseHandle's backstop retries. It never marks the handle
// flushed (the FS's Release backstop keys on the dirty flag alone), so
// commit-on-flush and the release backstop compose cleanly.
func (s *contentSource) FlushHandle(domain, name, token string) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	// Hold the commit gate's read side across resolve→commit so releaseAll cannot
	// discard this handle mid-append (see repoView.commitGate).
	rv.commitGate.RLock()
	defer rv.commitGate.RUnlock()
	fh, err := rv.resolve(token, name)
	if err != nil {
		return err
	}
	if rc := rv.fs.notesCommit(name, fh); rc != 0 {
		return errnoClass(rc)
	}
	return nil
}

func (s *contentSource) ReleaseHandle(domain, name, token string) error {
	rv, err := s.viewFor(domain)
	if err != nil {
		return err
	}
	// The backstop commits, so hold the commit gate's read side across it (see
	// repoView.commitGate).
	rv.commitGate.RLock()
	defer rv.commitGate.RUnlock()
	fh, err := rv.resolve(token, name)
	if err != nil {
		return err
	}
	// Release backstop-commits a dirty buffer no FlushHandle committed. ANY
	// failure — transient (EIO) or a deterministic rejection (EINVAL/EPERM) —
	// KEEPS the handle and its buffer (the edit's only copy), so KEEP the token
	// too and surface the errno: a re-issued close resolves the same fh and
	// retries. Only a clean release drops the handle, so drop the token then.
	rc := rv.fs.Release(name, fh)
	if rc != 0 {
		return errnoClass(rc)
	}
	rv.drop(token)
	return nil
}

// ReleaseAllHandles drops every open handle for domain on a holder-generation
// change, DISCARDING dirty buffers rather than committing them (see releaseAll):
// a mid-rewrite generation change would otherwise make a torn partial write
// canonical.
func (s *contentSource) ReleaseAllHandles(domain string) error {
	if rv := s.existingView(domain); rv != nil {
		rv.releaseAll()
	}
	return nil
}

// --- token bookkeeping ---

func (rv *repoView) mint(fh uint64, name string) string {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	tok := strconv.FormatUint(fh, 10)
	rv.tokens[tok] = tokenState{fh: fh, path: name}
	return tok
}

func (rv *repoView) resolve(token, name string) (uint64, error) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	ts, ok := rv.tokens[token]
	if !ok {
		// A stale token is almost always a contentd restart (the in-memory
		// handle table died with the process): map it to a LOUD transient IO
		// error, never ClassNotFound. ENOENT would surface an editor's save as
		// file-not-found on a file that exists. The lost in-flight buffer is
		// inherent — it lived only in the dead process — but the KeepAlive
		// LaunchAgent keeps the window short and durability lives in the store,
		// so the editor's retry (reopen) reconstructs the handle.
		return 0, &treeErr{class: content.ClassTransient, msg: "unknown handle token " + token + " (contentd restarted?)"}
	}
	if ts.path != name {
		return 0, &treeErr{class: content.ClassInvalid, msg: fmt.Sprintf("token %s bound to %s, not %s", token, ts.path, name)}
	}
	return ts.fh, nil
}

func (rv *repoView) drop(token string) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	delete(rv.tokens, token)
}

func (rv *repoView) releaseAll() {
	// Take the commit gate's WRITE side first: block any new commit and drain
	// in-flight ones, so no commit's store append can land concurrently with (or
	// after) the discards below and make a torn partial write canonical. A commit
	// that had not yet started finds its token gone here and is refused before its
	// append — never a post-discard commit.
	rv.commitGate.Lock()
	defer rv.commitGate.Unlock()
	// Advance the generation so an OpenHandle that snapshotted the old one and
	// blocked on the gate refuses transient rather than minting a handle for this
	// closing generation.
	rv.gen.Add(1)
	rv.mu.Lock()
	toks := rv.tokens
	rv.tokens = map[string]tokenState{}
	rv.mu.Unlock()
	for _, ts := range toks {
		// DISCARD, never commit: a holder-generation change can land mid-rewrite,
		// and the normal Release backstop would make a parse-valid partial prefix
		// canonical (a torn write). The editor re-opens against the new generation.
		rv.fs.discardHandle(ts.path, ts.fh)
	}
}

// treeErr carries a bridge error class so the errno class crosses the wire; the
// holder maps it back to the fuse errno (ClassNotFound→ENOENT, ClassInvalid→
// EINVAL, ClassPerm→EPERM, everything else→EIO).
type treeErr struct {
	class string
	msg   string
}

func (e *treeErr) Error() string { return e.msg }
func (e *treeErr) Class() string { return e.class }

// errnoClass maps the FS's cgofuse errno onto a bridge error class. The FS has
// already logged the underlying failure to stderr (its operator channel), so
// only the class need cross the wire.
func errnoClass(rc int) error {
	switch -rc {
	case fuse.ENOENT:
		return &treeErr{class: content.ClassNotFound, msg: "no such path"}
	case fuse.EINVAL:
		return &treeErr{class: content.ClassInvalid, msg: "rejected: unparseable or invalid"}
	case fuse.EPERM, fuse.EACCES:
		return &treeErr{class: content.ClassPerm, msg: "operation not permitted"}
	default:
		return &treeErr{class: content.ClassTransient, msg: fmt.Sprintf("errno %d", -rc)}
	}
}
