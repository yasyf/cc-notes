package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

type capturedImportPolicy struct {
	token    model.SHA
	manifest gitobj.SourceManifest
	digest   [32]byte
}

func (p capturedImportPolicy) SourceProvenance(_ context.Context, token model.SHA, manifest gitobj.SourceManifest) (sourceindex.ImportProvenance, error) {
	if token != p.token || !manifest.Equal(p.manifest) {
		return sourceindex.ImportProvenance{}, errors.New("unexpected historical token or manifest")
	}
	return sourceindex.ImportProvenance{Evidence: "fixture previously captured observable descriptors", RenderPolicy: sourceview.RenderPolicy, ObjectDigest: p.digest}, nil
}

func TestHistoricalAppliedImportPinsAggregateAcrossGCAndRepeatImport(t *testing.T) {
	s := initStore(t)
	snapshot := create(t, s, noteOps("historical receipt body"))
	ref := refs.For(model.KindNote, snapshot.EntityID())
	tip := snapshot.Meta().Head
	captured, err := s.SourceHead(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var digest [32]byte
	if err := s.Repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		var err error
		digest, err = projectionDigest(sourcegraph.New(objects), captured.ObjectRoot)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	empty, err := s.Repo.WriteSourceManifestCommit(t.Context(), "", gitobj.SourceManifest{})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := gitobj.NewSourceManifest(map[string]model.SHA{ref: tip})
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Repo.WriteSourceManifestCommit(t.Context(), empty, manifest)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("7", 64)
	request := sha256.Sum256([]byte("historical receipt request"))
	receipt := sha256.Sum256([]byte("historical receipt digest"))
	oldProof, err := s.Repo.WriteSourceOperationProof(t.Context(), gitobj.SourceOperationProof{OperationID: id, State: gitobj.SourceOperationApplied, Expected: empty, Committed: token, Result: "historical-result", RequestDigest: request, RetainedTips: []model.SHA{tip}})
	if err != nil {
		t.Fatal(err)
	}
	opref, _ := sourceindex.OperationRef(id)
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{{Ref: sourceindex.LegacyRef, New: token}, {Ref: opref, New: oldProof}, {Ref: sourceindex.OperationPinRef(id), New: oldProof}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRef(t.Context(), ref, "", tip); err != nil {
		t.Fatal(err)
	}
	if err := s.ReindexWithSourcePolicy(t.Context(), capturedImportPolicy{token: token, manifest: manifest, digest: digest}); err != nil {
		t.Fatal(err)
	}
	operation, found, err := s.ReadOperation(t.Context(), id)
	if err != nil || !found {
		t.Fatalf("imported operation=%+v found=%v err=%v", operation, found, err)
	}
	if operation.Proof == oldProof || operation.Proof != operation.Pin || operation.Token != token || operation.Previous != empty || operation.Result != "historical-result" || operation.RequestDigest != request {
		t.Fatalf("migrated operation=%+v old proof=%s", operation, oldProof)
	}
	dir := s.Git.Dir
	gittest.Git(t, dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, dir, "gc", "--prune=now")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Reindex(t.Context()); err != nil {
		t.Fatalf("repeat imported Applied reindex after GC=%v", err)
	}
	current, found, err := s.ReadOperation(t.Context(), id)
	if err != nil || !found || current.Proof != operation.Proof || current.RetentionRoot != operation.RetentionRoot {
		t.Fatalf("repeat receipt=%+v found=%v err=%v want %+v", current, found, err, operation)
	}
	if err := s.Repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		commit, err := objects.ReadCommit(operation.Proof)
		if err != nil {
			return err
		}
		if len(commit.Parents) != 2 || commit.Parents[1] != operation.RetentionRoot {
			return fmt.Errorf("Applied parents=%v want token+aggregate", commit.Parents)
		}
		_, err = objects.ReadCommit(operation.RetentionRoot)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	descriptor, stream, err := s.OpenSourceContent(t.Context(), token, "entity:note:"+string(snapshot.EntityID()))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if descriptor.Hash != sha256.Sum256(sourceview.CodecOf(model.KindNote).Render(snapshot)) {
		t.Fatalf("historical bytes descriptor=%+v", descriptor)
	}
	if _, err := s.SettleSourceOperation(t.Context(), id, request, receipt, gitobj.SourceOperationAcknowledged); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, dir, "gc", "--prune=now")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Repo.ReadChain(t.Context(), tip); !errors.Is(err, gitobj.ErrIncompleteChain) {
		t.Fatalf("acknowledged raw tip retained: %v", err)
	}
	if err := s.Reindex(t.Context()); err != nil {
		t.Fatalf("acknowledged metadata import=%v", err)
	}
}
