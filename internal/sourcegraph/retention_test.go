package sourcegraph

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/model"
)

func rawRetentionTip(t *testing.T, repo *gitobj.Repo, title, nonce string) model.SHA {
	t.Helper()
	sig := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
	tip, err := repo.WriteOpsCommit(t.Context(), nil, sig, "retention fixture", model.Pack{Lamport: 1, Ops: []model.Op{model.CreateNote{Nonce: nonce, Title: title, Body: title + " body"}}})
	if err != nil {
		t.Fatal(err)
	}
	return tip
}

func TestSourceGraphRetentionCanonicalAndSharedUpdates(t *testing.T) {
	repo, _ := graphFixture(t)
	a := rawRetentionTip(t, repo, "a", strings.Repeat("1", 32))
	b := rawRetentionTip(t, repo, "b", strings.Repeat("2", 32))
	oid := fmt.Sprintf("%x", sha256.Sum256([]byte("same oid")))
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := NewRetention(objects)
		first, err := graph.Put(RetentionRoot{}, "a", RetainedEntity{Tip: a, LFS: []gitobj.SourceOperationLFS{{OID: oid, Size: 8}, {OID: oid, Size: 9}, {OID: oid, Size: 8}, {OID: oid, Size: 0}}})
		if err != nil {
			return err
		}
		both, err := graph.Put(first, "b", RetainedEntity{Tip: b})
		if err != nil {
			return err
		}
		reverse, err := graph.Put(RetentionRoot{}, "b", RetainedEntity{Tip: b})
		if err != nil {
			return err
		}
		entity, found, err := graph.Get(first, "a")
		if err != nil || !found || entity.Tip != a || len(entity.LFS) != 3 {
			t.Fatalf("retention declaration=%+v/%t/%v", entity, found, err)
		}
		reverse, err = graph.Put(reverse, "a", entity)
		if err != nil {
			return err
		}
		if both != reverse {
			t.Fatalf("retention roots depend on insertion order: %v %v", both, reverse)
		}
		if err := graph.Validate(both); err != nil {
			return err
		}
		one, err := graph.Delete(both, "b")
		if err != nil {
			return err
		}
		if one != first {
			t.Fatalf("removing unrelated key changed old aggregate: got=%v want=%v", one, first)
		}
		if count, err := graph.Count(both); err != nil || count != 2 {
			t.Fatalf("retained count=%d/%v, want2", count, err)
		}
		leaf := RetentionRoot{Index: first.Index, Commit: both.Commit}
		if _, err := graph.Count(leaf); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("accepted mismatched aggregate root: %v", err)
		}
		empty, err := graph.Delete(first, "a")
		if err != nil || empty != (RetentionRoot{}) {
			t.Fatalf("last deletion=%+v/%v", empty, err)
		}
		return nil
	})
}

func TestSourceGraphRetentionByteKeysRoundTrip(t *testing.T) {
	repo, _ := graphFixture(t)
	tip := rawRetentionTip(t, repo, "byte keys", strings.Repeat("7", 32))
	keys := []string{"", "a\x00b", "é", string([]byte{0xff}), string([]byte{0xff, 0x00, 0xfe})}
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := NewRetention(objects)
		var root, reverse RetentionRoot
		for _, key := range keys {
			var err error
			root, err = graph.Put(root, key, RetainedEntity{Tip: tip})
			if err != nil {
				return err
			}
			entity, found, err := graph.Get(root, key)
			if err != nil || !found || entity.Tip != tip {
				t.Fatalf("byte key %x failed roundtrip: %+v/%t/%v", []byte(key), entity, found, err)
			}
			if err := graph.Validate(root); err != nil {
				return err
			}
		}
		for i := len(keys) - 1; i >= 0; i-- {
			var err error
			reverse, err = graph.Put(reverse, keys[i], RetainedEntity{Tip: tip})
			if err != nil {
				return err
			}
		}
		if root != reverse {
			t.Fatalf("byte-key insertion order changed roots: %v %v", root, reverse)
		}
		for _, key := range keys {
			var err error
			root, err = graph.Delete(root, key)
			if err != nil {
				return err
			}
			if _, found, err := graph.Get(root, key); err != nil || found {
				t.Fatalf("deleted byte key %x remains: %t/%v", []byte(key), found, err)
			}
			if err := graph.Validate(root); err != nil {
				return err
			}
		}
		if root != (RetentionRoot{}) {
			t.Fatalf("byte-key final deletion retained %v", root)
		}
		return nil
	})
}

func TestSourceGraphRetentionRejectsWrongObjectKindsAndMissingObjects(t *testing.T) {
	repo, _ := graphFixture(t)
	tip := rawRetentionTip(t, repo, "typed retention", strings.Repeat("8", 32))
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		blob, err := objects.WriteBlob([]byte("not a raw entity commit"))
		if err != nil {
			return err
		}
		missing := model.SHA(strings.Repeat("e", 40))
		for _, test := range []struct {
			name   string
			entity RetainedEntity
		}{
			{"sealed commit", RetainedEntity{Tip: tip, Blobs: []model.SHA{tip}}},
			{"sealed missing", RetainedEntity{Tip: tip, Blobs: []model.SHA{missing}}},
			{"raw blob", RetainedEntity{Tip: blob}},
			{"raw missing", RetainedEntity{Tip: missing}},
		} {
			if _, err := NewRetention(objects).Put(RetentionRoot{}, "typed", test.entity); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("admitted %s: %v", test.name, err)
			}
			root, err := forgedRetentionRoot(objects, "typed", test.entity)
			if err != nil {
				return err
			}
			if _, _, err := NewRetention(objects).Get(root, "typed"); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Get accepted forged %s: %v", test.name, err)
			}
			if err := NewRetention(objects).Validate(root); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Validate accepted forged %s: %v", test.name, err)
			}
		}
		valid, err := NewRetention(objects).Put(RetentionRoot{}, "typed", RetainedEntity{Tip: tip, Blobs: []model.SHA{blob}})
		if err != nil {
			return err
		}
		return NewRetention(objects).Validate(valid)
	})
}

func TestSourceGraphRetentionMetadataDoesNotPinReleasedContent(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		if os.Getenv("CCN_REQUIRE_SOURCEGRAPH_LFS") == "1" {
			t.Fatal(err)
		}
		t.Skip("git-lfs not installed")
	}
	repo, dir := graphFixture(t)
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "initial")
	a := rawRetentionTip(t, repo, "changed", strings.Repeat("3", 32))
	b := rawRetentionTip(t, repo, "untouched", strings.Repeat("4", 32))
	body := []byte("untouched receipt payload")
	oid := fmt.Sprintf("%x", sha256.Sum256(body))
	_, common := gittest.Dirs(t, dir)
	content := lfs.Store{Dir: filepath.Join(common, "lfs")}
	if err := content.PutVerified(bytes.NewReader(body), oid, int64(len(body))); err != nil {
		t.Fatal(err)
	}
	var both, onlyB RetentionRoot
	var sealed model.SHA
	sealedBody := []byte("authenticated imported sealed body")
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := NewRetention(objects)
		var err error
		sealed, err = objects.WriteBlob(sealedBody)
		if err != nil {
			return err
		}
		both, err = graph.Put(RetentionRoot{}, "a", RetainedEntity{Tip: a})
		if err != nil {
			return err
		}
		both, err = graph.Put(both, "b", RetainedEntity{Tip: b, LFS: []gitobj.SourceOperationLFS{{OID: oid, Size: int64(len(body))}}, Blobs: []model.SHA{sealed}})
		if err != nil {
			return err
		}
		onlyB, err = graph.Delete(both, "a")
		if err != nil {
			return err
		}
		if err := graph.Validate(both); err != nil {
			return err
		}
		return graph.Validate(onlyB)
	})
	const metadata = "refs/cc-notes-sourcegraph/metadata"
	const receiptA = "refs/heads/cc-notes-receipt-pins/sourcegraph-a"
	const receiptB = "refs/heads/cc-notes-receipt-pins/sourcegraph-b"
	gittest.Git(t, dir, "update-ref", metadata, string(both.Index))
	gittest.Git(t, dir, "update-ref", receiptA, string(both.Commit))
	gittest.Git(t, dir, "update-ref", receiptB, string(onlyB.Commit))
	pruneRetentionFixture(t, dir)
	assertRawRetention(t, dir, a, true)
	assertRawRetention(t, dir, b, true)
	assertSealedRetention(t, dir, sealed, sealedBody, true)
	if !content.Has(oid) {
		t.Fatal("prune removed untouched Applied LFS content")
	}
	if listed := gittest.Git(t, dir, "lfs", "ls-files", "--all"); !strings.Contains(listed, oid[:10]) {
		t.Fatalf("LFS did not discover aggregate pointer %q", listed)
	}
	gittest.Git(t, dir, "update-ref", "-d", receiptA, string(both.Commit))
	pruneRetentionFixture(t, dir)
	assertRawRetention(t, dir, a, false)
	assertRawRetention(t, dir, b, true)
	assertSealedRetention(t, dir, sealed, sealedBody, true)
	if !content.Has(oid) {
		t.Fatal("first acknowledgement removed another receipt's shared LFS content")
	}
	gittest.Git(t, dir, "update-ref", "-d", receiptB, string(onlyB.Commit))
	pruneRetentionFixture(t, dir)
	assertRawRetention(t, dir, b, false)
	assertSealedRetention(t, dir, sealed, sealedBody, false)
	if content.Has(oid) {
		t.Fatal("metadata-only graph retained released LFS content")
	}
	repo, err := gitobj.Open(gittest.Dirs(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := NewRetention(objects)
		count, err := graph.Count(both)
		if err != nil || count != 2 {
			t.Fatalf("metadata disappeared with released payloads: count=%d/%v", count, err)
		}
		return nil
	})
}

func TestSourceGraphRetentionRejectsExtraPayloadParents(t *testing.T) {
	repo, _ := graphFixture(t)
	a := rawRetentionTip(t, repo, "a", strings.Repeat("5", 32))
	b := rawRetentionTip(t, repo, "b", strings.Repeat("6", 32))
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := NewRetention(objects)
		root, err := graph.Put(RetentionRoot{}, "a", RetainedEntity{Tip: a})
		if err != nil {
			return err
		}
		commit, err := objects.ReadCommit(root.Commit)
		if err != nil {
			return err
		}
		commit.Parents = append(commit.Parents, b)
		broken, err := objects.WriteCommit(commit)
		if err != nil {
			return err
		}
		n, err := graph.index.load(root.Index)
		if err != nil {
			return err
		}
		brokenNode := *n
		brokenNode.Aggregate = broken
		data, err := json.Marshal(brokenNode.header)
		if err != nil {
			return err
		}
		blob, err := objects.WriteBlob(data)
		if err != nil {
			return err
		}
		entries, err := objects.ReadTree(root.Index)
		if err != nil {
			return err
		}
		for i := range entries {
			if entries[i].Name == "node" {
				entries[i].Hash = plumbing.NewHash(string(blob))
			}
		}
		index, err := objects.WriteTree(entries)
		if err != nil {
			return err
		}
		if err := NewRetention(objects).Validate(RetentionRoot{Index: index, Commit: broken}); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("accepted an extra historical payload parent: %v", err)
		}
		return nil
	})
}

func pruneRetentionFixture(t *testing.T, dir string) {
	t.Helper()
	gittest.Git(t, dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, dir, "gc", "--prune=now")
	gittest.Git(t, dir, "fsck", "--strict", "--no-dangling")
	gittest.Git(t, dir, "lfs", "prune")
}

func assertRawRetention(t *testing.T, dir string, tip model.SHA, want bool) {
	t.Helper()
	repo, err := gitobj.Open(gittest.Dirs(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	chain, err := repo.ReadChain(t.Context(), tip)
	if want {
		if err != nil || len(chain) != 1 || chain[0].SHA != tip {
			t.Fatalf("retained raw tip=%s chain=%v err=%v", tip, chain, err)
		}
	} else if !errors.Is(err, gitobj.ErrIncompleteChain) {
		t.Fatalf("released raw tip=%s chain=%v err=%v", tip, chain, err)
	}
}

func assertSealedRetention(t *testing.T, dir string, blob model.SHA, want []byte, present bool) {
	t.Helper()
	repo, err := gitobj.Open(gittest.Dirs(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		data, err := objects.ReadBlob(blob)
		if present {
			if err != nil || !bytes.Equal(data, want) {
				t.Fatalf("retained sealed body=%q/%v, want=%q", data, err, want)
			}
		} else if !errors.Is(err, plumbing.ErrObjectNotFound) {
			t.Fatalf("released sealed body=%q/%v, want missing", data, err)
		}
		return nil
	})
}

func forgedRetentionRoot(objects *gitobj.SourceObjects, key string, entity RetainedEntity) (RetentionRoot, error) {
	data, err := json.Marshal(retentionLeaf{Format: retentionLeafFormat, KeyHex: hexKey(key), Entity: entity})
	if err != nil {
		return RetentionRoot{}, err
	}
	entries := make([]object.TreeEntry, 0, len(entity.Blobs))
	for _, blob := range entity.Blobs {
		entries = append(entries, object.TreeEntry{Name: "sealed-" + string(blob), Mode: filemode.Regular, Hash: plumbing.NewHash(string(blob))})
	}
	leaf, err := writeRetentionCommit(objects, data, []model.SHA{entity.Tip}, entries)
	if err != nil {
		return RetentionRoot{}, err
	}
	value, err := objects.WriteBlob([]byte(leaf))
	if err != nil {
		return RetentionRoot{}, err
	}
	graph := NewRetention(objects)
	index, err := graph.index.mutate("", hexKey(key), value, leaf, false)
	if err != nil {
		return RetentionRoot{}, err
	}
	return graph.root(index)
}
