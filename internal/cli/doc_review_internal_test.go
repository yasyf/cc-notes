package cli

import (
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func TestDocVerdict(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	cases := []struct {
		name string
		doc  model.Doc
		want string
	}{
		{"out-of-date flag is EXPIRED", model.Doc{StaleAt: now.Unix(), VerifiedAt: now.Unix()}, verdictExpired},
		{"never verified is UNVERIFIED", model.Doc{}, verdictUnverified},
		{"verified within threshold is fresh", model.Doc{VerifiedAt: now.Add(-time.Minute).Unix()}, ""},
		{"verified past threshold is STALE", model.Doc{VerifiedAt: now.Add(-2 * time.Hour).Unix()}, verdictStale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// EXPIRED/UNVERIFIED short-circuit before any git read; STALE and
			// the fresh case skip drift detection against an unborn HEAD, so the
			// nil store is never touched.
			got, err := docVerdict(t.Context(), nil, "", tc.doc, now, time.Hour, false)
			if err != nil {
				t.Fatalf("docVerdict: %v", err)
			}
			if got != tc.want {
				t.Fatalf("verdict = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("witness mismatch against live content is DRIFTED", func(t *testing.T) {
		dir := t.TempDir()
		driftRepoInit(t, dir)
		commitDirFile(t, dir, "internal/auth/login.go", "v1\n")
		t.Chdir(dir)

		s, err := store.Open(dir)
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		ctx := t.Context()
		head, err := resolveHead(ctx, s)
		if err != nil {
			t.Fatalf("resolveHead: %v", err)
		}
		anchors := []model.Anchor{{Kind: model.AnchorDir, Value: "internal/auth"}}
		witness, err := buildWitness(ctx, s, head, anchors)
		if err != nil {
			t.Fatalf("buildWitness: %v", err)
		}
		// Verified just now so the verdict reaches the drift check rather than
		// short-circuiting to UNVERIFIED or STALE.
		doc := model.Doc{Anchors: anchors, Witness: witness, VerifiedAt: time.Now().Unix()}

		commitDirFile(t, dir, "internal/auth/login.go", "v2\n")
		head, err = resolveHead(ctx, s)
		if err != nil {
			t.Fatalf("resolveHead after edit: %v", err)
		}
		got, err := docVerdict(ctx, s, head, doc, time.Now(), time.Hour, false)
		if err != nil {
			t.Fatalf("docVerdict: %v", err)
		}
		if got != verdictDrifted {
			t.Fatalf("verdict = %q, want %q", got, verdictDrifted)
		}
	})
}

func TestDocReviewCount(t *testing.T) {
	dir := t.TempDir()
	driftRepoInit(t, dir)
	commitDirFile(t, dir, "internal/api/client.go", "v1\n")
	t.Chdir(dir)

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := t.Context()
	head, err := resolveHead(ctx, s)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}
	anchors := []model.Anchor{{Kind: model.AnchorDir, Value: "internal/api"}}

	// An unverified doc needs review.
	if _, err := s.Create(ctx, []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: "unverified", Anchors: anchors}}); err != nil {
		t.Fatalf("create unverified doc: %v", err)
	}

	// A born-verified doc against unchanged content is fresh — not counted.
	freshSnap, err := s.Create(ctx, []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: "fresh", Anchors: anchors}})
	if err != nil {
		t.Fatalf("create fresh doc: %v", err)
	}
	fresh := freshSnap.(model.Doc)
	witness, err := buildWitness(ctx, s, head, fresh.Anchors)
	if err != nil {
		t.Fatalf("buildWitness: %v", err)
	}
	if _, err := s.Append(ctx, refs.For(model.KindDoc, fresh.ID), []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}}); err != nil {
		t.Fatalf("verify fresh doc: %v", err)
	}

	count, err := docReviewCount(ctx, s, head, time.Now(), defaultNoteStaleAfter)
	if err != nil {
		t.Fatalf("docReviewCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("docReviewCount = %d, want 1 (only the unverified doc)", count)
	}
}
