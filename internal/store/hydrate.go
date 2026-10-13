package store

import (
	"context"
	"strings"

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

func (s *Store) hydrateChain(ctx context.Context, tip model.SHA) error {
	remotes, err := s.promisorRemotes(ctx)
	if err != nil {
		return err
	}
	for _, remote := range remotes {
		if err := s.Hydrate(ctx, remote, []model.SHA{tip}); err != nil {
			return err
		}
	}
	return nil
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
