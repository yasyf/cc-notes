// Package store is the entity layer: each note or task lives as a chain of
// immutable operation commits on its own ref. Create roots a chain, Append
// extends it under ref compare-and-swap with bounded retries, Load and the
// List methods fold chains into snapshots, Resolve expands short id
// prefixes, and Merge writes the union merge commit sync uses for diverged
// replicas.
//
// A store spans two repository roles. The records repository holds entity
// refs, operation commits, folds, attachments, and source-index objects,
// reached through Repo (gitobj) and RecordsGit (gitcmd). The context checkout
// the store was opened at supplies HEAD, branches, commits, trees, working
// files, config settings, and author identity, reached through ContextRepo and
// Git. A checkout with no cc-notes.storage binding is its own records
// repository; a bound one — a thin clone sharing a full repository's corpus —
// reads and writes records in the backend the binding names, validated at open
// and rechecked at every records operation. internal/sync composes the records
// handles directly for fetch, push, ref listing, and chain reads.
package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

// actorEnv overrides the git author identity when set; its value must be
// "Name <email>".
const actorEnv = "CC_NOTES_ACTOR"

// sessionEnv overrides the Claude session id stamped on each write; when
// unset, claudeSessionEnv (set by Claude Code itself) is the fallback.
// sessionEnv set but empty suppresses stamping.
const (
	sessionEnv       = "CC_NOTES_SESSION_ID"
	claudeSessionEnv = "CLAUDE_CODE_SESSION_ID"
)

const (
	// maxAttempts bounds the Append compare-and-swap retry loop.
	maxAttempts = 16
	// backoffBase is the minimum sleep between Append attempts; the jittered
	// component doubles per attempt, capped at backoffBase << backoffCapShift.
	backoffBase     = time.Millisecond
	backoffCapShift = 6
	// listConcurrency bounds the chain-loading fan-out of the List methods.
	listConcurrency = 8
)

var (
	// ErrContended reports an Append that lost the ref compare-and-swap on
	// every attempt.
	ErrContended = errors.New("ref contended")
	// ErrNotFound reports a Resolve prefix matching no entity.
	ErrNotFound = errors.New("entity not found")
	// ErrAmbiguous reports a Resolve prefix matching more than one entity;
	// the concrete error is an *AmbiguousError carrying the candidates.
	ErrAmbiguous = errors.New("ambiguous entity prefix")
	// ErrDuplicate reports a Create whose content exactly duplicates a live
	// entity; the concrete error is a *DuplicateError carrying the survivor.
	ErrDuplicate = errors.New("duplicate entity")
)

// Candidate is one entity matched by an ambiguous Resolve prefix.
type Candidate struct {
	ID    model.EntityID
	Title string
}

// AmbiguousError reports the candidates matching an ambiguous Resolve
// prefix, ordered by id. It matches ErrAmbiguous under errors.Is.
type AmbiguousError struct {
	Kind       model.Kind
	Prefix     string
	Candidates []Candidate
}

// Error lists every candidate's short id and title.
func (e *AmbiguousError) Error() string {
	parts := make([]string, len(e.Candidates))
	for i, c := range e.Candidates {
		parts[i] = fmt.Sprintf("%s %q", c.ID.Short(), c.Title)
	}
	return fmt.Sprintf("ambiguous %s prefix %q: %s", e.Kind, e.Prefix, strings.Join(parts, ", "))
}

// Is reports whether target is ErrAmbiguous.
func (e *AmbiguousError) Is(target error) bool { return target == ErrAmbiguous }

// DuplicateError reports that Create found a live entity of Kind whose folded
// content equals the create pack's and returned Existing instead of a twin.
type DuplicateError struct {
	Kind     model.Kind
	Existing model.Snapshot
}

// Error names the reused entity's kind and short id.
func (e *DuplicateError) Error() string {
	return fmt.Sprintf("exact duplicate of %s %s", e.Kind, e.Existing.EntityID().Short())
}

// Is reports whether target is ErrDuplicate.
func (e *DuplicateError) Is(target error) bool { return target == ErrDuplicate }

// Store reads and writes entities. Records — entity refs, op commits, folds,
// attachments, source-index objects — live in the records repository; HEAD,
// branches, commits, trees, working files, config settings, and author identity
// come from the context checkout the store was opened at. A context with no
// storage binding is its own records repository.
type Store struct {
	// Repo is the records object database.
	Repo *gitobj.Repo
	// Git runs git in the context checkout.
	Git gitcmd.Git
	// RecordsGit runs git against the records repository: entity ref CAS, sync,
	// notes refspecs, the LFS endpoint, and the prune guard.
	RecordsGit gitcmd.Git
	// ContextRepo is the context object database: HEAD, ancestry, path and tree
	// witnesses.
	ContextRepo *gitobj.Repo

	// now stamps commit signatures; tests freeze it.
	now func() time.Time
	// cache is the local, tip-keyed fold accelerator under the records common
	// directory. It lives outside refs/cc-notes/* and is never pushed.
	cache *foldCache
	// relevant caches relevance results under the context common directory.
	relevant         *lruDir
	gitDir           string
	commonDir        string
	recordsCommonDir string
	bare             bool
	root             *rootMemo
	policy           *policyMemo
	pins             map[string]model.SHA
	// storage is the context's binding record; Pinned views share it.
	storage *storageBinding
}

type rootMemo struct {
	once sync.Once
	path string
	err  error
}

type policyMemo struct {
	once  sync.Once
	value LocalPolicy
	err   error
}

// Open opens the git repository containing dir, following worktree and
// subdirectory indirection. The author identity is resolved lazily, on each
// write: the CC_NOTES_ACTOR environment variable ("Name <email>") when set —
// a malformed value is an error, never a fallback — otherwise git's author
// identity for the repository. Each write also stamps the Claude session id
// from CC_NOTES_SESSION_ID, falling back to CLAUDE_CODE_SESSION_ID, omitted
// when neither is set.
func Open(dir string) (*Store, error) {
	return OpenContext(context.Background(), dir)
}

// OpenContext is Open with an explicit context for repository discovery. The
// context is discovered first; a cc-notes.storage binding in its common config
// is then validated and the records repository opened from it. A binding that
// is malformed, or names a backend that is missing, replaced, bound onward, or
// unreadable, is a *BindingError — never a fallback to a context-local corpus.
func OpenContext(ctx context.Context, dir string) (*Store, error) {
	git := gitcmd.Git{Dir: dir}
	gitDir, commonDir, bare, err := git.Dirs(ctx)
	if err != nil {
		return nil, fmt.Errorf("open git repository at %s: %w", dir, err)
	}
	contextRepo, err := gitobj.Open(gitDir, commonDir)
	if err != nil {
		return nil, fmt.Errorf("open git repository at %s: %w", dir, err)
	}
	s := &Store{
		Repo:             contextRepo,
		Git:              git,
		RecordsGit:       git,
		ContextRepo:      contextRepo,
		now:              time.Now,
		gitDir:           gitDir,
		commonDir:        commonDir,
		recordsCommonDir: commonDir,
		bare:             bare,
		root:             &rootMemo{},
		policy:           &policyMemo{},
	}
	s.storage, err = openBinding(commonDir)
	if err != nil {
		return nil, err
	}
	if s.storage.bound {
		b := s.storage.binding
		records, err := openRecords(b)
		if err != nil {
			return nil, s.storage.fail(err)
		}
		s.Repo = records
		s.RecordsGit = gitcmd.Backend(b.CommonDir)
		s.recordsCommonDir = b.CommonDir
	}
	s.cache = newFoldCache(filepath.Join(s.recordsCommonDir, foldCacheSubdir), foldCacheCap)
	s.relevant = &lruDir{capacity: relevantCacheCap, dir: filepath.Join(commonDir, relevantCacheSubdir)}
	return s, nil
}

// Pinned returns a view of the store whose listings fold exactly the entity
// refs in tips, keyed by full ref name, instead of enumerating the
// repository. Every other handle and cache is shared with s.
func (s *Store) Pinned(tips map[string]model.SHA) *Store {
	return &Store{
		Repo:             s.Repo,
		Git:              s.Git,
		RecordsGit:       s.RecordsGit,
		ContextRepo:      s.ContextRepo,
		now:              s.now,
		cache:            s.cache,
		relevant:         s.relevant,
		gitDir:           s.gitDir,
		commonDir:        s.commonDir,
		recordsCommonDir: s.recordsCommonDir,
		bare:             s.bare,
		root:             s.root,
		policy:           s.policy,
		pins:             tips,
		storage:          s.storage,
	}
}

// CommonDir returns the context's absolute shared git directory.
func (s *Store) CommonDir() string { return s.commonDir }

// RecordsCommonDir returns the records repository's absolute common directory:
// the backend's when bound, the context's otherwise.
func (s *Store) RecordsCommonDir() string { return s.recordsCommonDir }

// Binding returns the validated storage binding the store opened under, or
// ok=false when the context is its own records repository.
func (s *Store) Binding() (Binding, bool) { return s.storage.binding, s.storage.bound }

// CheckRecords re-validates the records backend at an operation boundary. A
// bound store stats the context config (re-reading the binding only when that
// stat moved) and the backend directory against the bound device/inode. An
// unbound store stats the config alone, so a binding published after open
// surfaces as ErrBindingChanged instead of a silent write into the context.
// Every failure is a *BindingError. It never spawns git.
func (s *Store) CheckRecords() error { return s.storage.check() }

// PublishRef is the one path every records ref publication takes: the exact
// compare-and-swap through RecordsGit, then CheckRecords again, so a binding
// that changed while the ref was being written surfaces as a *BindingError
// naming the published ref instead of a record silently hidden behind a new
// binding. A failed compare-and-swap returns gitcmd's error unchanged.
func (s *Store) PublishRef(ctx context.Context, ref string, newSHA, old model.SHA) error {
	if err := s.RecordsGit.UpdateRef(ctx, ref, newSHA, old); err != nil {
		return err
	}
	if err := s.CheckRecords(); err != nil {
		return fmt.Errorf("published %s: %w", ref, err)
	}
	return nil
}

// GitDir returns the absolute per-worktree git directory, the one holding this
// worktree's HEAD.
func (s *Store) GitDir() string { return s.gitDir }

// Bare reports whether the repository has no worktree.
func (s *Store) Bare() bool { return s.bare }

// Root returns the absolute worktree root, resolved once per store.
func (s *Store) Root(ctx context.Context) (string, error) {
	s.root.once.Do(func() { s.root.path, s.root.err = s.Git.Root(ctx) })
	return s.root.path, s.root.err
}

func (s *Store) signature(ctx context.Context) (gitobj.Signature, model.Actor, error) {
	name, email, err := s.actor(ctx)
	if err != nil {
		return gitobj.Signature{}, "", err
	}
	return gitobj.Signature{Name: name, Email: email, When: s.now()}, model.Actor(name + " <" + email + ">"), nil
}

// Actor returns the identity that signs this store's writes — the
// CC_NOTES_ACTOR override when set, otherwise git's author identity — as
// "Name <email>". Ops that embed an actor (claim) must carry exactly this
// value.
func (s *Store) Actor(ctx context.Context) (model.Actor, error) {
	name, email, err := s.actor(ctx)
	if err != nil {
		return "", err
	}
	return model.Actor(name + " <" + email + ">"), nil
}

func (s *Store) actor(ctx context.Context) (name, email string, err error) {
	if value, ok := os.LookupEnv(actorEnv); ok {
		return parseActor(value)
	}
	return s.Git.AuthorIdent(ctx)
}

func session() string {
	if value, ok := os.LookupEnv(sessionEnv); ok {
		return value
	}
	return os.Getenv(claudeSessionEnv)
}

func parseActor(value string) (name, email string, err error) {
	i := strings.IndexByte(value, '<')
	j := strings.LastIndexByte(value, '>')
	if i < 0 || j < i || j != len(value)-1 {
		return "", "", fmt.Errorf("%s %q: want \"Name <email>\"", actorEnv, value)
	}
	if name, email = strings.TrimSpace(value[:i]), strings.TrimSpace(value[i+1:j]); name == "" || email == "" {
		return "", "", fmt.Errorf("%s %q: want \"Name <email>\"", actorEnv, value)
	}
	return name, email, nil
}

// Backoff sleeps the jittered, exponentially-growing delay for the given
// retry attempt, honoring ctx cancellation. Append and internal/sync share
// it between ref compare-and-swap attempts.
func Backoff(ctx context.Context, attempt int) error {
	limit := backoffBase << min(attempt, backoffCapShift)
	select {
	case <-ctx.Done():
		return ctx.Err()
	//nolint:gosec // G404: retry-backoff jitter is timing, not security; a weak PRNG is appropriate.
	case <-time.After(backoffBase + rand.N(limit)):
		return nil
	}
}

func nextLamport(chain []model.PackCommit) model.Lamport {
	var top model.Lamport
	for _, c := range chain {
		top = max(top, c.Pack.Lamport)
	}
	return top + 1
}

// roundTrip re-decodes the pack's wire form, so an op that would fail the
// codec's validation can never be published to a ref, and folds see exactly
// what a future reader will decode.
func roundTrip(pack model.Pack) (model.Pack, error) {
	data, err := pack.MarshalJSON()
	if err != nil {
		return model.Pack{}, err
	}
	return model.DecodePack(data)
}
