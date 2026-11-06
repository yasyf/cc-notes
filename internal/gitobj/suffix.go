package gitobj

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/model"
)

const checkpointFile = "checkpoint.json"

// ReadSuffix decodes every commit reachable from tip through commits whose
// lamport is above floor, breadth-first, and never reads below them. It
// returns those commits and, with its lamport, each parent where the walk
// stopped because its lamport is at most floor. A missing object fails with
// ErrIncompleteChain.
func (r *Repo) ReadSuffix(ctx context.Context, tip model.SHA, floor model.Lamport) (_ []model.PackCommit, _ map[model.SHA]model.Lamport, resultErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beginRead(ctx)
	defer r.endRead(&resultErr)
	if err := r.refreshGraft(); err != nil {
		return nil, nil, err
	}
	var suffix []model.PackCommit
	stops := map[model.SHA]model.Lamport{}
	queue := []model.SHA{tip}
	seen := map[model.SHA]bool{tip: true}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		pc, err := r.decodeCommit(queue[0])
		if err != nil {
			return nil, nil, err
		}
		queue = queue[1:]
		if pc.Pack.Lamport <= floor {
			stops[pc.SHA] = pc.Pack.Lamport
			continue
		}
		suffix = append(suffix, pc)
		for _, parent := range pc.Parents {
			if !seen[parent] {
				seen[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	return suffix, stops, nil
}

// Reaches reports whether every target is from or an ancestor of it. It walks
// back from from only through commits whose lamport is at least the lowest
// target's, so it reads the band of history between from and the targets. A
// target it does not meet in that band reads as unreached.
func (r *Repo) Reaches(ctx context.Context, from model.SHA, targets map[model.SHA]model.Lamport) (_ bool, resultErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beginRead(ctx)
	defer r.endRead(&resultErr)
	if err := r.refreshGraft(); err != nil {
		return false, err
	}
	low := model.Lamport(0)
	first := true
	for _, lamport := range targets {
		if first || lamport < low {
			low, first = lamport, false
		}
	}
	missing := len(targets)
	queue := []model.SHA{from}
	seen := map[model.SHA]bool{from: true}
	for len(queue) > 0 && missing > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		pc, err := r.decodeCommit(queue[0])
		if err != nil {
			return false, err
		}
		queue = queue[1:]
		if _, ok := targets[pc.SHA]; ok {
			missing--
		}
		if pc.Pack.Lamport <= low {
			continue
		}
		for _, parent := range pc.Parents {
			if !seen[parent] {
				seen[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	return missing == 0, nil
}

func (r *Repo) decodeCommit(sha model.SHA) (model.PackCommit, error) {
	if !plumbing.IsHash(string(sha)) {
		return model.PackCommit{}, fmt.Errorf("invalid sha %q", sha)
	}
	commit, err := r.lookupCommit(plumbing.NewHash(string(sha)))
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return model.PackCommit{}, fmt.Errorf("%w: commit %s %s", ErrIncompleteChain, sha, r.missingSuffix())
	}
	if err != nil {
		return model.PackCommit{}, fmt.Errorf("read commit %s: %w", sha, err)
	}
	return r.packCommit(commit)
}

// WriteCheckpointCommit stores data as the one file of a parentless commit and
// returns its sha. It writes objects only; the caller moves the checkpoint ref.
func (r *Repo) WriteCheckpointCommit(ctx context.Context, data []byte) (model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	blob, err := r.writeBlob(data)
	if err != nil {
		return "", fmt.Errorf("write checkpoint blob: %w", err)
	}
	tree, err := r.writeObject(&object.Tree{Entries: []object.TreeEntry{{Name: checkpointFile, Mode: filemode.Regular, Hash: blob}}})
	if err != nil {
		return "", fmt.Errorf("write checkpoint tree: %w", err)
	}
	identity := object.Signature{Name: "CC Notes", Email: "checkpoint@cc-notes.invalid", When: time.Unix(0, 0).UTC()}
	sha, err := r.writeObject(&object.Commit{Author: identity, Committer: identity, Message: "cc-notes: checkpoint", TreeHash: tree})
	if err != nil {
		return "", fmt.Errorf("write checkpoint commit: %w", err)
	}
	return model.SHA(sha.String()), nil
}

// ReadCheckpointCommit returns the file WriteCheckpointCommit stored in sha.
func (r *Repo) ReadCheckpointCommit(ctx context.Context, sha model.SHA) (_ []byte, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beginRead(ctx)
	defer r.endRead(&resultErr)
	commit, err := r.commit(sha)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint %s: %w", sha, err)
	}
	file, err := retry(r, func() (*object.File, error) { return commit.File(checkpointFile) })
	if err != nil {
		return nil, fmt.Errorf("read checkpoint %s: %w", sha, err)
	}
	reader, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("read checkpoint %s: %w", sha, err)
	}
	data, err := io.ReadAll(reader)
	if err = errors.Join(err, reader.Close()); err != nil {
		return nil, fmt.Errorf("read checkpoint %s: %w", sha, err)
	}
	return data, nil
}
