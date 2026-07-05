package gitobj

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/model"
)

// CodeCommit is one code commit's graph-relevant fields; Summary is the message's first line.
type CodeCommit struct {
	SHA        model.SHA
	Parents    []model.SHA
	Author     model.Actor
	AuthorTime int64
	CommitTime int64
	Summary    string
}

// WalkCommits walks the commit DAG from tips, newest-first by commit time, stopping at limit commits (limit <= 0 means unbounded) or at commits older than since (unix seconds, 0 = unbounded). A parent object missing from the ODB (shallow clone) truncates the walk at that edge rather than failing.
func (r *Repo) WalkCommits(ctx context.Context, tips []model.SHA, limit int, since int64) ([]CodeCommit, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	frontier := &commitHeap{}
	seen := make(map[plumbing.Hash]bool)
	for _, tip := range tips {
		if !plumbing.IsHash(string(tip)) {
			return nil, false, fmt.Errorf("invalid tip sha %q", tip)
		}
		hash := plumbing.NewHash(string(tip))
		if seen[hash] {
			continue
		}
		commit, err := object.GetCommit(r.repo.Storer, hash)
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, false, fmt.Errorf("%w: %s", ErrCommitNotFound, tip)
		}
		if err != nil {
			return nil, false, fmt.Errorf("read commit %s: %w", tip, err)
		}
		seen[hash] = true
		heap.Push(frontier, commit)
	}
	var out []CodeCommit
	truncated := false
	for frontier.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if limit > 0 && len(out) == limit {
			if (*frontier)[0].Committer.When.Unix() >= since {
				truncated = true
			}
			break
		}
		commit := heap.Pop(frontier).(*object.Commit)
		if commit.Committer.When.Unix() < since {
			break
		}
		out = append(out, newCodeCommit(commit))
		for _, parent := range commit.ParentHashes {
			if seen[parent] {
				continue
			}
			pc, err := object.GetCommit(r.repo.Storer, parent)
			if errors.Is(err, plumbing.ErrObjectNotFound) {
				truncated = true
				continue
			}
			if err != nil {
				return nil, false, fmt.Errorf("read commit %s: %w", parent, err)
			}
			seen[parent] = true
			heap.Push(frontier, pc)
		}
	}
	return out, truncated, nil
}

// FirstParentMerges returns the merge commits (more than one parent) on tip's first-parent path, newest first, bounded by limit and since.
func (r *Repo) FirstParentMerges(ctx context.Context, tip model.SHA, limit int, since int64) ([]CodeCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !plumbing.IsHash(string(tip)) {
		return nil, fmt.Errorf("invalid tip sha %q", tip)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, err := object.GetCommit(r.repo.Storer, plumbing.NewHash(string(tip)))
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrCommitNotFound, tip)
	}
	if err != nil {
		return nil, fmt.Errorf("read commit %s: %w", tip, err)
	}
	var merges []CodeCommit
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cur.Committer.When.Unix() < since {
			break
		}
		if len(cur.ParentHashes) > 1 {
			merges = append(merges, newCodeCommit(cur))
			if limit > 0 && len(merges) == limit {
				break
			}
		}
		if len(cur.ParentHashes) == 0 {
			break
		}
		first := cur.ParentHashes[0]
		next, err := object.GetCommit(r.repo.Storer, first)
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read commit %s: %w", first, err)
		}
		cur = next
	}
	return merges, nil
}

func newCodeCommit(c *object.Commit) CodeCommit {
	var parents []model.SHA
	for _, p := range c.ParentHashes {
		parents = append(parents, model.SHA(p.String()))
	}
	return CodeCommit{
		SHA:        model.SHA(c.Hash.String()),
		Parents:    parents,
		Author:     model.Actor(fmt.Sprintf("%s <%s>", c.Author.Name, c.Author.Email)),
		AuthorTime: c.Author.When.Unix(),
		CommitTime: c.Committer.When.Unix(),
		Summary:    summary(c.Message),
	}
}

func summary(message string) string {
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		return message[:i]
	}
	return message
}

// commitHeap orders commits newest-first by commit time, breaking ties by SHA
// so the priority walk is deterministic; it is a max-heap over container/heap's
// min-heap, so Less reports whether i pops before j.
type commitHeap []*object.Commit

func (h commitHeap) Len() int { return len(h) }

func (h commitHeap) Less(i, j int) bool {
	ti, tj := h[i].Committer.When.Unix(), h[j].Committer.When.Unix()
	if ti != tj {
		return ti > tj
	}
	return h[i].Hash.String() > h[j].Hash.String()
}

func (h commitHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *commitHeap) Push(x any) { *h = append(*h, x.(*object.Commit)) }

func (h *commitHeap) Pop() any {
	old := *h
	n := len(old)
	c := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return c
}
