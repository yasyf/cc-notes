package fusefs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/sourcedriver"
)

const (
	gitDriverID           = "cc-notes-git-v1"
	gitDriverConfigPrefix = "cc-notes.git-driver-config.v1\x00"
)

func newGitDriverDeclaration(
	authority causal.SourceAuthorityID,
	repoRoot string,
) (catalog.SourceAuthorityDeclaration, error) {
	if err := causal.ValidateSourceAuthorityID(authority); err != nil {
		return catalog.SourceAuthorityDeclaration{}, fmt.Errorf("cc-notes source: invalid authority: %w", err)
	}
	config, err := newGitDriverConfig(repoRoot)
	if err != nil {
		return catalog.SourceAuthorityDeclaration{}, err
	}
	digest := gitDriverDeclarationDigest(authority, config)
	return catalog.SourceAuthorityDeclaration{
		Authority: authority, DriverID: gitDriverID, DriverConfig: config,
		DeclarationDigest: digest,
	}, nil
}

func newGitDriverConfig(repoRoot string) ([]byte, error) {
	if !exactAbsolutePath(repoRoot) || strings.IndexFunc(repoRoot, unicode.IsControl) >= 0 {
		return nil, errors.New("cc-notes source: repository root is not an exact absolute path")
	}
	config := append([]byte(gitDriverConfigPrefix), repoRoot...)
	if len(config) > catalog.SourceDriverConfigMaxBytes {
		return nil, errors.New("cc-notes source: Git driver config exceeds the exact wire bound")
	}
	return config, nil
}

func parseGitDriverConfig(config []byte) (string, error) {
	if len(config) > catalog.SourceDriverConfigMaxBytes || !bytes.HasPrefix(config, []byte(gitDriverConfigPrefix)) {
		return "", errors.New("cc-notes source: unknown Git driver config")
	}
	repoRoot := string(config[len(gitDriverConfigPrefix):])
	canonical, err := newGitDriverConfig(repoRoot)
	if err != nil || !bytes.Equal(config, canonical) {
		return "", errors.New("cc-notes source: Git driver config is not canonical")
	}
	return repoRoot, nil
}

func gitDriverDeclarationDigest(authority causal.SourceAuthorityID, config []byte) [sha256.Size]byte {
	input := make([]byte, 0, len(authority)+len(gitDriverID)+len(config)+64)
	input = append(input, "cc-notes.git-driver.declaration.v1\x00"...)
	input = append(input, authority...)
	input = append(input, 0)
	input = append(input, gitDriverID...)
	input = append(input, 0)
	input = append(input, config...)
	return sha256.Sum256(input)
}

func newGitSourceDriver(
	_ context.Context,
	invocation holder.SourceDriverInvocation,
) (sourcedriver.Driver, error) {
	if invocation.DriverID != gitDriverID || invocation.FleetOwner != catalog.SourceAuthorityFleetOwnerID("cc-notes") ||
		causal.ValidateSourceAuthorityID(invocation.Authority) != nil || invocation.AuthorityGeneration == 0 ||
		invocation.TargetsDigest == ([sha256.Size]byte{}) {
		return nil, errors.New("cc-notes source: invalid FuseKit Git driver invocation")
	}
	repoRoot, err := parseGitDriverConfig(invocation.DriverConfig)
	if err != nil {
		return nil, err
	}
	if gitDriverDeclarationDigest(invocation.Authority, invocation.DriverConfig) != invocation.DeclarationDigest {
		return nil, errors.New("cc-notes source: FuseKit driver declaration differs from its canonical identity")
	}
	return NewGitDriver(
		invocation.Authority, invocation.AuthorityGeneration, invocation.DeclarationDigest, repoRoot,
	)
}

// NewGitDriverFactories returns cc-notes' exact fixed semantic driver registry.
func NewGitDriverFactories() (holder.DriverFactories, error) {
	return holder.NewDriverFactories(map[string]holder.DriverFactory{
		gitDriverID: {Semantic: newGitSourceDriver},
	})
}
