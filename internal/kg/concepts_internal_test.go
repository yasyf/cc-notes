package kg

import (
	"slices"
	"testing"
)

func TestConceptsExtractsIdentifierShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"backticked span", "see `internal/kg/build.go` for it", []string{"internal/kg/build.go"}},
		{"camel case", "the SourceDigest call", []string{"sourcedigest"}},
		{"snake case", "set concept_df_cap now", []string{"concept_df_cap"}},
		{"kebab case", "the capt-hook pack", []string{"capt-hook"}},
		{"dotted path", "read model.Anchor here", []string{"model.anchor"}},
		{"slash path", "under internal/kg/eval today", []string{"internal/kg/eval"}},
		{"prose only", "the graph is already connected", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := concepts(tc.text); !slices.Equal(got, tc.want) {
				t.Fatalf("concepts(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestConceptsRejectsBareVersions pins the mandatory version filter: bare
// release and date strings were what survived the encoder's precision collapse
// and they name no topic.
func TestConceptsRejectsBareVersions(t *testing.T) {
	for _, text := range []string{"pinned at 0.16.0", "tagged v0.21.3", "on 2026-07-13", "bumped to v1.2"} {
		if got := concepts(text); len(got) != 0 {
			t.Errorf("concepts(%q) = %v, want none", text, got)
		}
	}
	if got := concepts("released capt-hook v0.21.3"); !slices.Equal(got, []string{"capt-hook"}) {
		t.Fatalf("concepts kept the wrong spans: %v", got)
	}
}

func TestConceptsSkipsQuotedSentences(t *testing.T) {
	long := "read `the graph is already connected and saturated` here"
	if got := concepts(long); len(got) != 0 {
		t.Fatalf("concepts(%q) = %v, want none: a five-word backtick span is prose", long, got)
	}
}

func TestConceptDFCapScalesWithCorpus(t *testing.T) {
	for _, tc := range []struct{ entities, want int }{
		{0, conceptDFFloor},
		{5, conceptDFFloor},
		{97, 19},
		{124, 24},
	} {
		if got := conceptDFCap(tc.entities); got != tc.want {
			t.Errorf("conceptDFCap(%d) = %d, want %d", tc.entities, got, tc.want)
		}
	}
}

// TestDiscriminatingDropsSingletonsAndHubs pins both mandatory filters: a term
// only one entity mentions creates no edge, and a term most of them mention —
// the repository's own name — relates everything and so discriminates nothing.
func TestDiscriminatingDropsSingletonsAndHubs(t *testing.T) {
	perEntity := map[NodeID][]string{}
	for i := range 100 {
		id := NodeID("note:" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		terms := []string{"cc-notes"}
		if i < 10 {
			terms = append(terms, "sandsql")
		}
		if i == 0 {
			terms = append(terms, "mentioned-once")
		}
		perEntity[id] = terms
	}
	df := discriminating(perEntity, len(perEntity))
	if _, ok := df["cc-notes"]; ok {
		t.Error("kept the repository-name hub (df=100 of 100)")
	}
	if _, ok := df["mentioned-once"]; ok {
		t.Error("kept a singleton, which can create no edge")
	}
	if got := df["sandsql"]; got != 10 {
		t.Errorf("sandsql df = %d, want 10", got)
	}
}
