package gitobj_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func retirementGit(t *testing.T, dir, objects string, input []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), "GIT_OBJECT_DIRECTORY="+objects,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return out
}

func retirementNativePack(t *testing.T, dir, objects, packs string, oids []string, keep bool) string {
	t.Helper()
	data := retirementGit(t, dir, objects, []byte(strings.Join(oids, "\n")+"\n"),
		"pack-objects", "--stdout", "--threads=1", "--window=0")
	stage := filepath.Join(t.TempDir(), "objects")
	stagePacks := filepath.Join(stage, "pack")
	if err := os.MkdirAll(stagePacks, 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{"index-pack", "--stdin", "--promisor", "--rev-index", "--threads=1"}
	if keep {
		args = append(args, "--keep=reader-retirement-test")
	}
	result := strings.Fields(string(retirementGit(t, dir, stage, data, args...)))
	if len(result) != 2 || (result[0] != "pack" && result[0] != "keep") || len(result[1]) != 40 {
		t.Fatalf("index-pack receipt = %q", result)
	}
	stem := "pack-" + result[1]
	suffixes := []string{".promisor", ".rev", ".idx", ".pack"}
	if keep {
		suffixes = append([]string{".keep"}, suffixes...)
	}
	for _, suffix := range suffixes {
		content, err := os.ReadFile(filepath.Join(stagePacks, stem+suffix))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(packs, stem+suffix), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := os.ReadFile(filepath.Join(packs, stem+".idx"))
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(string(retirementGit(t, dir, objects, index, "show-index"))), "\n")
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		fields := strings.Fields(row)
		if len(fields) < 2 {
			t.Fatalf("show-index row = %q", row)
		}
		got = append(got, fields[1])
	}
	want := slices.Clone(oids)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("pack OIDs = %v, want %v", got, want)
	}
	return stem
}

func TestPersistentReaderAfterNativePromisorPackRetirement(t *testing.T) {
	dir := initRepo(t)
	_, common := gittest.Dirs(t, dir)
	objects := filepath.Join(common, "objects")
	packs := filepath.Join(objects, "pack")
	writer := open(t, dir)
	var parents []model.SHA
	var want []model.PackCommit
	var oids []string
	for i := range 10 {
		pack := model.Pack{Lamport: model.Lamport(i + 1), Ops: []model.Op{model.SetBody{Body: fmt.Sprintf("retirement-body-%d", i)}}}
		when := t0.Add(time.Duration(i) * time.Minute)
		sha := write(t, writer, parents, when, pack)
		want = append(want, model.PackCommit{SHA: sha, Parents: slices.Clone(parents), Author: testActor, AuthorTime: when.Unix(), Pack: pack})
		oids = append(oids, string(sha), git(t, dir, "rev-parse", string(sha)+"^{tree}"), git(t, dir, "rev-parse", string(sha)+":ops.json"))
		parents = []model.SHA{sha}
	}
	slices.Reverse(want)
	slices.Sort(oids)
	if len(slices.Compact(slices.Clone(oids))) != 30 {
		t.Fatalf("expected 30 distinct commit/tree/blob OIDs, got %v", oids)
	}
	old := make([]string, 0, len(oids))
	for _, oid := range oids {
		old = append(old, retirementNativePack(t, dir, objects, packs, []string{oid}, false))
	}
	assertPackCount := func(count int) {
		t.Helper()
		files, err := filepath.Glob(filepath.Join(packs, "*.pack"))
		if err != nil || len(files) != count {
			t.Fatalf("pack count = %d, want %d: %v", len(files), count, err)
		}
	}
	assertPackCount(30)
	for _, oid := range oids {
		if err := os.Remove(filepath.Join(objects, oid[:2], oid[2:])); err != nil {
			t.Fatal(err)
		}
	}
	assertNoLoose := func() {
		t.Helper()
		for _, oid := range oids {
			if _, err := os.Stat(filepath.Join(objects, oid[:2], oid[2:])); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("loose object %s unexpectedly exists: %v", oid, err)
			}
		}
	}
	assertNoLoose()
	reader := open(t, dir)
	settlePacks(t, dir)
	assertChain := func() {
		t.Helper()
		got, err := reader.ReadChain(t.Context(), want[0].SHA)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("persistent ReadChain = %+v, want %+v: %v", got, want, err)
		}
	}
	assertChain()
	assertChain()
	rollup := retirementNativePack(t, dir, objects, packs, oids, true)
	cohort := append(slices.Clone(old), rollup)
	for i := range cohort {
		cohort[i] += ".idx"
	}
	slices.Sort(cohort)
	retirementGit(t, dir, objects, []byte(strings.Join(cohort, "\n")+"\n"),
		"-c", "core.multiPackIndex=true", "-c", "midx.version=1", "multi-pack-index", "write",
		"--no-bitmap", "--stdin-packs", "--preferred-pack="+rollup+".pack")
	retirementGit(t, dir, objects, nil, "-c", "core.multiPackIndex=true", "-c", "midx.version=1", "multi-pack-index", "expire")
	for _, stem := range old {
		for _, suffix := range []string{".pack", ".idx"} {
			if _, err := os.Stat(filepath.Join(packs, stem+suffix)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old %s%s was not retired: %v", stem, suffix, err)
			}
		}
	}
	assertPackCount(1)
	assertChain()
	assertChain()
	fresh, err := open(t, dir).ReadChain(t.Context(), want[0].SHA)
	if err != nil || !reflect.DeepEqual(fresh, want) {
		t.Fatalf("fresh ReadChain = %+v, want %+v: %v", fresh, want, err)
	}
	rows := strings.Split(strings.TrimSpace(string(retirementGit(t, dir, objects, []byte(strings.Join(oids, "\n")+"\n"), "cat-file", "--batch-check"))), "\n")
	if len(rows) != 30 {
		t.Fatalf("native oracle returned %d objects", len(rows))
	}
	for i, row := range rows {
		fields := strings.Fields(row)
		if len(fields) != 3 || fields[0] != oids[i] || fields[1] == "missing" {
			t.Fatalf("native oracle row = %q for %s", row, oids[i])
		}
	}
	assertNoLoose()
	t.Logf("retired %d single-object promisor packs; persistent and fresh readers verified all %d OIDs", len(old), len(oids))
}
