package fusefs

import (
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/causal"
)

func TestMergeDesiredDeclarationIsOrderedExactAndNonDestructive(t *testing.T) {
	first := protocolDeclarationForTest("cc-notes:first", "one")
	third := protocolDeclarationForTest("cc-notes:third", "three")
	second := protocolDeclarationForTest("cc-notes:second", "two")
	current := []catalogproto.SourceAuthorityDeclaration{first, third}
	merged, changed, err := mergeDesiredDeclaration(current, second)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !reflect.DeepEqual(merged, []catalogproto.SourceAuthorityDeclaration{first, second, third}) {
		t.Fatalf("merged = %+v changed=%t", merged, changed)
	}
	if !reflect.DeepEqual(current, []catalogproto.SourceAuthorityDeclaration{first, third}) {
		t.Fatalf("input mutated = %+v", current)
	}
	idempotent, changed, err := mergeDesiredDeclaration(merged, second)
	if err != nil || changed || !reflect.DeepEqual(idempotent, merged) {
		t.Fatalf("idempotent merge = %+v changed=%t err=%v", idempotent, changed, err)
	}
	conflict := second
	conflict.DriverConfig = []byte("different")
	if _, _, err := mergeDesiredDeclaration(merged, conflict); err == nil {
		t.Fatal("conflicting immutable declaration was accepted")
	}
}

func TestProtocolFleetStateBindsEveryExactDeclaration(t *testing.T) {
	declarations := []catalogproto.SourceAuthorityDeclaration{
		protocolDeclarationForTest("cc-notes:first", "one"),
		protocolDeclarationForTest("cc-notes:second", "two"),
	}
	state := protocolFleetStateForTest(t, declarations)
	if err := validateProtocolFleetState(state, declarations); err != nil {
		t.Fatal(err)
	}
	tampered := append([]catalogproto.SourceAuthorityDeclaration(nil), declarations...)
	tampered[1].DriverConfig = []byte("different")
	if err := validateProtocolFleetState(state, tampered); err == nil {
		t.Fatal("fleet state accepted different declarations")
	}
}

func protocolDeclarationForTest(authority, config string) catalogproto.SourceAuthorityDeclaration {
	return catalogproto.SourceAuthorityDeclaration{
		Authority: catalogproto.SourceAuthorityID(authority), DriverID: gitDriverID,
		DriverConfig: []byte(config), DeclarationDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func protocolFleetStateForTest(
	t *testing.T,
	declarations []catalogproto.SourceAuthorityDeclaration,
) catalogproto.DesiredSourceFleetState {
	t.Helper()
	authorities := make([]causal.SourceAuthorityID, len(declarations))
	catalogDeclarations := make([]catalog.SourceAuthorityDeclaration, len(declarations))
	for index, declaration := range declarations {
		authorities[index] = causal.SourceAuthorityID(declaration.Authority)
		catalogDeclarations[index] = catalog.SourceAuthorityDeclaration{
			Authority: authorities[index], DriverID: declaration.DriverID,
			DriverConfig: append([]byte(nil), declaration.DriverConfig...),
		}
		digest, err := hex.DecodeString(declaration.DeclarationDigest)
		if err != nil {
			t.Fatal(err)
		}
		copy(catalogDeclarations[index].DeclarationDigest[:], digest)
	}
	authoritiesDigest, err := catalog.SourceAuthorityFleetDigest(authorities)
	if err != nil {
		t.Fatal(err)
	}
	declarationsDigest, err := catalog.SourceAuthorityFleetDeclarationsDigest(catalogDeclarations)
	if err != nil {
		t.Fatal(err)
	}
	return catalogproto.DesiredSourceFleetState{
		Owner: string(helperOwner), Generation: 1, AuthorityCount: uint64(len(declarations)),
		AuthoritiesDigest:  hex.EncodeToString(authoritiesDigest[:]),
		DeclarationsDigest: hex.EncodeToString(declarationsDigest[:]),
	}
}
