package fusefs

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/holder"
)

func TestGitDriverFactoryResolvesOneFixedCanonicalDeclaration(t *testing.T) {
	authority := causal.SourceAuthorityID("cc-notes:shared")
	declaration, err := newGitDriverDeclaration(authority, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if declaration.DriverID != gitDriverID || declaration.DeclarationDigest == ([sha256.Size]byte{}) {
		t.Fatalf("declaration = %+v", declaration)
	}
	targetsDigest := sha256.Sum256([]byte("targets"))
	invocation := holder.SourceDriverInvocation{
		DriverID: declaration.DriverID, Authority: authority, FleetOwner: "cc-notes",
		AuthorityGeneration: 1, DeclarationDigest: declaration.DeclarationDigest,
		DriverConfig: declaration.DriverConfig, TargetsDigest: targetsDigest,
	}
	factories, err := NewGitDriverFactories()
	if err != nil {
		t.Fatal(err)
	}
	driver, err := factories.SourceDriver(t.Context(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	gitDriver, ok := driver.(*GitDriver)
	if !ok || gitDriver.authority != authority || gitDriver.authorityGeneration != 1 ||
		gitDriver.declarationDigest != invocation.DeclarationDigest || gitDriver.repoRoot != string(declaration.DriverConfig[len(gitDriverConfigPrefix):]) {
		t.Fatalf("resolved driver = %#v", driver)
	}

	mismatches := []struct {
		name   string
		mutate func(*holder.SourceDriverInvocation)
	}{
		{name: "driver", mutate: func(value *holder.SourceDriverInvocation) { value.DriverID = "foreign" }},
		{name: "owner", mutate: func(value *holder.SourceDriverInvocation) { value.FleetOwner = "foreign" }},
		{name: "authority", mutate: func(value *holder.SourceDriverInvocation) { value.Authority = "foreign" }},
		{name: "generation", mutate: func(value *holder.SourceDriverInvocation) { value.AuthorityGeneration = 0 }},
		{name: "declaration", mutate: func(value *holder.SourceDriverInvocation) { value.DeclarationDigest[0] ^= 0xff }},
		{name: "config", mutate: func(value *holder.SourceDriverInvocation) { value.DriverConfig = append(value.DriverConfig, 'x') }},
		{name: "targets", mutate: func(value *holder.SourceDriverInvocation) { value.TargetsDigest = [32]byte{} }},
	}
	for _, mismatch := range mismatches {
		t.Run("reject_"+mismatch.name, func(t *testing.T) {
			candidate := invocation
			candidate.DriverConfig = append([]byte(nil), invocation.DriverConfig...)
			mismatch.mutate(&candidate)
			if _, err := factories.SourceDriver(t.Context(), candidate); err == nil {
				t.Fatal("mismatched invocation was accepted")
			}
		})
	}
}

func TestGitDriverConfigIsExactAndBounded(t *testing.T) {
	authority := causal.SourceAuthorityID("cc-notes:bounded")
	declaration, err := newGitDriverDeclaration(authority, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, err := parseGitDriverConfig(declaration.DriverConfig)
	if err != nil {
		t.Fatal(err)
	}
	if repoRoot == "" {
		t.Fatal("repository root is empty")
	}
	malformed := [][]byte{
		nil,
		[]byte("cc-notes.git-driver-config.v0\x00/root"),
		append(append([]byte(nil), declaration.DriverConfig...), 0),
		[]byte(gitDriverConfigPrefix + "relative"),
		[]byte(gitDriverConfigPrefix + "/tmp/../repo"),
		[]byte(gitDriverConfigPrefix + "/tmp/repo\n"),
	}
	for index, config := range malformed {
		if _, err := parseGitDriverConfig(config); err == nil {
			t.Fatalf("malformed config %d accepted", index)
		}
	}
	if _, err := newGitDriverConfig("/" + strings.Repeat("x", catalog.SourceDriverConfigMaxBytes)); err == nil {
		t.Fatal("oversized config accepted")
	}
}
