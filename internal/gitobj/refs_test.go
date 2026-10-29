package gitobj

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

const testRefHash = "1234567890123456789012345678901234567890"

type countedReferenceReader struct {
	*bytes.Reader
	read  int
	calls int
}

func (r *countedReferenceReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.Reader.ReadAt(p, off)
	r.read += n
	r.calls++
	return n, err
}

func TestPackedReferencePrefix(t *testing.T) {
	for _, sorted := range []bool{false, true} {
		t.Run(fmt.Sprintf("sorted=%t", sorted), func(t *testing.T) {
			var data strings.Builder
			if sorted {
				data.WriteString("# pack-refs with: peeled fully-peeled sorted\n")
			}
			for i := range 120 {
				name := i
				if !sorted {
					name = 119 - i
				}
				fmt.Fprintf(&data, "%s refs/heads/branch-%03d\n", testRefHash, name)
				if i%3 == 0 {
					fmt.Fprintf(&data, "^%s\n", testRefHash)
				}
			}
			p, err := openPackedReferences(strings.NewReader(data.String()), int64(data.Len()))
			if err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"refs/heads/branch-000", "refs/heads/branch-057", "refs/heads/branch-119", "refs/heads/branch-01", "refs/heads/branch-120", "refs/aaa", "refs/zzz"} {
				t.Run(prefix, func(t *testing.T) {
					got, err := p.prefix(t.Context(), prefix)
					if err != nil {
						t.Fatal(err)
					}
					want := make(map[string]*plumbing.Reference)
					for i := range 120 {
						name := fmt.Sprintf("refs/heads/branch-%03d", i)
						if strings.HasPrefix(name, prefix) {
							want[name] = plumbing.NewHashReference(plumbing.ReferenceName(name), plumbing.NewHash(testRefHash))
						}
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("prefix %q: got %v, want %v", prefix, got, want)
					}
				})
			}
		})
	}
}

func TestSortedPackedReferenceReadsAreBounded(t *testing.T) {
	var data strings.Builder
	data.WriteString("# pack-refs with: peeled fully-peeled sorted\n")
	for i := range 20000 {
		fmt.Fprintf(&data, "%s refs/heads/branch-%05d\n", testRefHash, i)
		if i%7 == 0 {
			fmt.Fprintf(&data, "^%s\n", testRefHash)
		}
	}
	r := &countedReferenceReader{Reader: bytes.NewReader([]byte(data.String()))}
	p, err := openPackedReferences(r, int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.prefix(t.Context(), "refs/heads/branch-10003")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["refs/heads/branch-10003"].Hash().String() != testRefHash {
		t.Fatalf("lookup: %v", got)
	}
	if r.read > 1<<16 {
		t.Fatalf("point lookup read %d bytes from a %d-byte packed-refs file", r.read, data.Len())
	}
	r.read = 0
	ref, err := p.find(t.Context(), "refs/heads/branch-10003")
	if err != nil || ref.Hash().String() != testRefHash {
		t.Fatalf("exact lookup: %v, %v", ref, err)
	}
	if r.read > 1<<16 {
		t.Fatalf("exact lookup read %d bytes from a %d-byte packed-refs file", r.read, data.Len())
	}
}

func TestSortedPackedReferencePrefixStreamsRange(t *testing.T) {
	var data strings.Builder
	data.WriteString("# pack-refs with: peeled fully-peeled sorted\n")
	for i := range 20000 {
		fmt.Fprintf(&data, "%s refs/heads/branch-%05d\n", testRefHash, i)
		if i%7 == 0 {
			fmt.Fprintf(&data, "^%s\n", testRefHash)
		}
	}
	packed := data.String()
	for _, tt := range []struct {
		name, prefix string
		first, count int
	}{
		{"all", "refs/heads/", 0, 20000},
		{"middle", "refs/heads/branch-10", 10000, 1000},
		{"tail", "refs/heads/branch-19", 19000, 1000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &countedReferenceReader{Reader: bytes.NewReader([]byte(packed))}
			p, err := openPackedReferences(r, int64(len(packed)))
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.prefix(t.Context(), tt.prefix)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.count {
				t.Fatalf("prefix %q returned %d refs, want %d", tt.prefix, len(got), tt.count)
			}
			for i := tt.first; i < tt.first+tt.count; i++ {
				name := fmt.Sprintf("refs/heads/branch-%05d", i)
				ref, ok := got[name]
				if !ok || ref.Hash().String() != testRefHash {
					t.Fatalf("prefix %q missing expected %s: %v", tt.prefix, name, ref)
				}
			}
			first := strings.Index(packed, fmt.Sprintf("%s refs/heads/branch-%05d\n", testRefHash, tt.first))
			end := len(packed)
			if tt.first+tt.count < 20000 {
				end = strings.Index(packed, fmt.Sprintf("%s refs/heads/branch-%05d\n", testRefHash, tt.first+tt.count))
			}
			rangeBytes := end - first
			if r.read > rangeBytes+(1<<16) {
				t.Fatalf("prefix %q read %d bytes for %d-byte selected range", tt.prefix, r.read, rangeBytes)
			}
			if limit := 128 + (rangeBytes+4095)/4096; r.calls > limit {
				t.Fatalf("prefix %q made %d reads for %d-byte range, want at most %d", tt.prefix, r.calls, rangeBytes, limit)
			}
		})
	}
}

func TestUnsortedPackedReferencesStreamOnce(t *testing.T) {
	var data strings.Builder
	data.WriteString("# pack-refs with: peeled\n")
	for i := 19999; i >= 0; i-- {
		fmt.Fprintf(&data, "%s refs/heads/branch-%05d\n", testRefHash, i)
	}
	r := &countedReferenceReader{Reader: bytes.NewReader([]byte(data.String()))}
	p, err := openPackedReferences(r, int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := p.find(t.Context(), "refs/heads/branch-00000")
	if err != nil || ref.Hash().String() != testRefHash {
		t.Fatalf("unsorted lookup: %v, %v", ref, err)
	}
	if r.read > data.Len()+2048 {
		t.Fatalf("streaming lookup read %d bytes from %d-byte input", r.read, data.Len())
	}
}

func TestPackedReferenceMalformedAndCanceled(t *testing.T) {
	for _, data := range []string{"not-a-ref\n", "^" + testRefHash + "\n"} {
		p, err := openPackedReferences(strings.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.prefix(t.Context(), "refs/"); err == nil {
			t.Fatalf("accepted malformed packed ref %q", data)
		}
	}
	data := "# pack-refs with: sorted\n" + testRefHash + " refs/heads/main\n"
	p, err := openPackedReferences(strings.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.prefix(ctx, "refs/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup: %v", err)
	}
}

func TestNativeReferencesWithoutGit(t *testing.T) {
	dir := gittest.InitRepo(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "base")
	head := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/docs/packed", head)
	gittest.Git(t, dir, "pack-refs", "--all", "--prune")
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/docs/loose", head)
	gittest.Git(t, dir, "symbolic-ref", "refs/cc-notes/docs/symbolic", "refs/cc-notes/docs/packed")
	if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "broken"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(filepath.Join(dir, ".git"), filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, name := range []string{"HEAD", "refs/cc-notes/docs/packed", "refs/cc-notes/docs/loose", "refs/cc-notes/docs/symbolic"} {
		got, err := r.Tip(t.Context(), name)
		if err != nil || got != model.SHA(head) {
			t.Fatalf("Tip(%s) = %s, %v; want %s", name, got, err, head)
		}
	}
	got, err := r.ListPrefix(t.Context(), "refs/cc-notes/docs/")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]model.SHA{"refs/cc-notes/docs/packed": model.SHA(head), "refs/cc-notes/docs/loose": model.SHA(head)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListPrefix: got %v, want %v", got, want)
	}
	symbols, err := r.ListSymbolic(t.Context(), "refs/cc-notes/docs/")
	if err != nil || !reflect.DeepEqual(symbols, map[string]string{"refs/cc-notes/docs/symbolic": "refs/cc-notes/docs/packed"}) {
		t.Fatalf("ListSymbolic: %v, %v", symbols, err)
	}
	if _, err := r.Tip(t.Context(), "refs/cc-notes/docs/missing"); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("missing ref: %v", err)
	}
}

func TestReferenceGenerationAtomicUpdates(t *testing.T) {
	dir := gittest.InitRepo(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "base")
	r, err := Open(filepath.Join(dir, ".git"), filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	stamp := func() ReferenceGeneration {
		t.Helper()
		v, err := r.RefGeneration(t.Context(), "refs/cc-notes/docs/")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := stamp()
	if after := stamp(); after != before {
		t.Fatal("unchanged references changed generation")
	}
	if before.Key() != stamp().Key() {
		t.Fatal("unchanged references changed the persisted generation key")
	}
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/docs/one", "HEAD")
	afterCreate := stamp()
	if afterCreate == before {
		t.Fatal("creation did not change generation")
	}
	if afterCreate.Key() == before.Key() {
		t.Fatal("creation did not change the persisted generation key")
	}
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "next")
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/docs/one", "HEAD")
	afterReplace := stamp()
	if afterReplace == afterCreate {
		t.Fatal("atomic replacement did not change generation")
	}
	gittest.Git(t, dir, "pack-refs", "--all", "--prune")
	afterPack := stamp()
	if afterPack == afterReplace {
		t.Fatal("packing did not change generation")
	}
	gittest.Git(t, dir, "update-ref", "-d", "refs/cc-notes/docs/one")
	if afterDelete := stamp(); afterDelete == afterPack {
		t.Fatal("deletion did not change generation")
	}
}

func TestReferencePrefixIncludesHEAD(t *testing.T) {
	dir := gittest.InitRepo(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "base")
	head := gittest.Git(t, dir, "rev-parse", "HEAD")
	branch := gittest.Git(t, dir, "symbolic-ref", "HEAD")
	r, err := Open(filepath.Join(dir, ".git"), filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "H", "HEAD"} {
		got, err := r.ListSymbolic(t.Context(), prefix)
		if err != nil || got["HEAD"] != branch {
			t.Fatalf("symbolic HEAD prefix %q: %v, %v", prefix, got, err)
		}
	}
	gittest.Git(t, dir, "checkout", "--detach", "-q", head)
	t.Setenv("PATH", t.TempDir())
	for _, prefix := range []string{"", "H", "HEAD"} {
		got, err := r.ListPrefix(t.Context(), prefix)
		if err != nil || got["HEAD"] != model.SHA(head) {
			t.Fatalf("detached HEAD prefix %q: %v, %v", prefix, got, err)
		}
	}
}

func TestReferenceGenerationWriteWindow(t *testing.T) {
	taken := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"recent", time.Second, false},
		{"settled", 3 * time.Second, true},
		{"future", -time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := ReferenceGeneration{packed: referenceStamp{mtime: taken.Add(-tc.age).UnixNano()}}
			if got := g.SettledAt(taken); got != tc.want {
				t.Fatalf("SettledAt age %s: got %t, want %t", tc.age, got, tc.want)
			}
		})
	}
}
