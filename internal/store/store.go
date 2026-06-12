// Package store is the entity layer: each note or task lives as a chain of
// immutable operation commits on its own ref. Create roots a chain, Append
// extends it under ref compare-and-swap with bounded retries, Load and the
// List methods fold chains into snapshots, Resolve expands short id
// prefixes, and Merge writes the union merge commit sync uses for diverged
// replicas. Object access goes through the exported Repo (gitobj) and Git
// (gitcmd) handles; internal/sync composes them directly for fetch, push,
// ref listing, and chain reads.
package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
)

// actorEnv overrides the git author identity when set; its value must be
// "Name <email>".
const actorEnv = "CC_NOTES_ACTOR"

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
)

// Candidate is one entity matched by an ambiguous Resolve prefix.
type Candidate struct {
	ID    model.EntityID
	Title string
}

// AmbiguousError reports the candidates matching an ambiguous Resolve
// prefix, ordered by id. It matches ErrAmbiguous under errors.Is.
type AmbiguousError struct {
	Kind       refs.Kind
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

// Store reads and writes entities in one repository. Repo carries object
// writes and all reads; Git carries ref compare-and-swap, config, identity,
// and network operations. internal/sync composes both handles directly.
type Store struct {
	Repo *gitobj.Repo
	Git  gitcmd.Git

	// now stamps commit signatures; tests freeze it.
	now func() time.Time
}

// Open opens the git repository containing dir, following worktree and
// subdirectory indirection. The author identity is resolved lazily, on each
// write: the CC_NOTES_ACTOR environment variable ("Name <email>") when set —
// a malformed value is an error, never a fallback — otherwise git's author
// identity for the repository.
func Open(dir string) (*Store, error) {
	repo, err := gitobj.Open(dir)
	if err != nil {
		return nil, err
	}
	return &Store{Repo: repo, Git: gitcmd.Git{Dir: dir}, now: time.Now}, nil
}

func (s *Store) signature(ctx context.Context) (gitobj.Signature, model.Actor, error) {
	name, email, err := s.actor(ctx)
	if err != nil {
		return gitobj.Signature{}, "", err
	}
	return gitobj.Signature{Name: name, Email: email, When: s.now()}, model.Actor(name + " <" + email + ">"), nil
}

func (s *Store) actor(ctx context.Context) (name, email string, err error) {
	if value, ok := os.LookupEnv(actorEnv); ok {
		return parseActor(value)
	}
	return s.Git.AuthorIdent(ctx)
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

func backoff(ctx context.Context, attempt int) error {
	limit := backoffBase << min(attempt, backoffCapShift)
	select {
	case <-ctx.Done():
		return ctx.Err()
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
