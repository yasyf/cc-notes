package store

import (
	"context"
	"errors"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

// Hydrate fetches from remote every object the chains ending at tips lack, so
// a ref a partial clone fetched without its blobs reads again.
func (s *Store) Hydrate(ctx context.Context, remote string, tips []model.SHA) error {
	missing, err := s.RecordsGit.MissingObjects(ctx, tips)
	if err != nil || len(missing) == 0 {
		return err
	}
	return s.RecordsGit.FetchObjects(ctx, remote, missing)
}

// readChain reads the chain ending at tip, first hydrating it from the
// records repository's promisor remotes when a partial clone left it
// incomplete.
func (s *Store) readChain(ctx context.Context, tip model.SHA) ([]model.PackCommit, error) {
	chain, err := s.Repo.ReadChain(ctx, tip)
	if !errors.Is(err, gitobj.ErrIncompleteChain) {
		return chain, err
	}
	remotes, rerr := s.promisorRemotes(ctx)
	if rerr != nil {
		return nil, errors.Join(err, rerr)
	}
	if len(remotes) == 0 {
		return nil, err
	}
	for _, remote := range remotes {
		if herr := s.Hydrate(ctx, remote, []model.SHA{tip}); herr != nil {
			return nil, errors.Join(err, herr)
		}
	}
	return s.Repo.ReadChain(ctx, tip)
}

func (s *Store) promisorRemotes(ctx context.Context) ([]string, error) {
	pairs, err := s.RecordsGit.ConfigGetRegexp(ctx, `^remote\..*\.promisor$`)
	if err != nil {
		return nil, err
	}
	var remotes []string
	for _, pair := range pairs {
		if pair[1] == "true" {
			remotes = append(remotes, strings.TrimSuffix(strings.TrimPrefix(pair[0], "remote."), ".promisor"))
		}
	}
	return remotes, nil
}
