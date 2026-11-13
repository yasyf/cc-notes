package store

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/cc-notes/model"
)

const queryExecutableRequestEnv = "CCN_QUERY_EXECUTABLE_REQUEST"

type queryExecutableRequest struct {
	Repo    string
	Output  string
	Version string
}

type queryExecutableResult struct {
	Binding     string
	Path        string
	Identity    string
	GenericKey  string
	Generation  string
	Transaction int
	Count       uint64
	Checks      int64
}

func readQueryExecutableResult(t *testing.T, s *Store) queryExecutableResult {
	t.Helper()
	q, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := executableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ctx := &queryWorkContext{Context: t.Context()}
	result, hit, err := readQueryMetadataIndex(ctx, q, func(tx *bolt.Tx) (queryExecutableResult, error) {
		root, err := managedRoot(tx)
		if err != nil {
			return queryExecutableResult{}, err
		}
		return queryExecutableResult{
			Binding:     q.binding,
			Path:        q.indexPath(),
			Identity:    identity,
			GenericKey:  s.listCacheName(model.KindDoc, []byte("generic-result"), foldCacheGeneration, identity),
			Generation:  strconv.FormatUint(root.Sequence(), 10),
			Transaction: tx.ID(),
			Count:       queryMetadataKindTips(tx, q.binding, model.KindDoc).Sequence(),
		}, nil
	})
	if err != nil || !hit {
		t.Fatalf("query metadata = hit %t, error %v", hit, err)
	}
	result.Checks = ctx.checks.Load()
	return result
}

func TestQueryMetadataExecutableProcess(t *testing.T) {
	raw := os.Getenv(queryExecutableRequestEnv)
	if raw == "" {
		t.Skip("query executable helper")
	}
	var request queryExecutableRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	version.Version = request.Version
	s, err := OpenContext(t.Context(), request.Repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	result := readQueryExecutableResult(t, s)
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.Output, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyQueryExecutable(t *testing.T, destination string) {
	t.Helper()
	input, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := input.Close(); err != nil {
			t.Error(err)
		}
	}()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		t.Fatal(err)
	}
}

func runQueryExecutable(t *testing.T, executable string, request queryExecutableRequest) queryExecutableResult {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestQueryMetadataExecutableProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), queryExecutableRequestEnv+"="+string(data))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("query child: %v\n%s", err, output)
	}
	data, err = os.ReadFile(request.Output)
	if err != nil {
		t.Fatal(err)
	}
	var result queryExecutableResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestQueryMetadataExecutableIndependentReuse(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "unrelated-query-executable")
	copyQueryExecutable(t, executable)
	for _, count := range []int{32, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := initStore(t)
			signature := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
			packed := make([]string, 0, count)
			for i := range count {
				tip, err := s.Repo.WriteOpsCommit(t.Context(), nil, signature, "doc fixture", model.Pack{Lamport: 1, Ops: docOps(fmt.Sprintf("fixture %d", i))})
				if err != nil {
					t.Fatal(err)
				}
				packed = append(packed, string(tip)+" "+refs.For(model.KindDoc, model.EntityID(tip)))
			}
			slices.SortFunc(packed, func(a, b string) int { return strings.Compare(strings.Fields(a)[1], strings.Fields(b)[1]) })
			if err := os.WriteFile(filepath.Join(s.recordsCommonDir, "packed-refs"), []byte("# pack-refs with: peeled fully-peeled sorted \n"+strings.Join(packed, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := s.Reindex(t.Context()); err != nil {
				t.Fatal(err)
			}
			first := runQueryExecutable(t, os.Args[0], queryExecutableRequest{Repo: s.Git.Dir, Output: filepath.Join(t.TempDir(), "first.json"), Version: "same-semantics-first"})
			second := runQueryExecutable(t, executable, queryExecutableRequest{Repo: s.Git.Dir, Output: filepath.Join(t.TempDir(), "second.json"), Version: "same-semantics-unrelated"})
			if first.Identity == second.Identity || first.GenericKey == second.GenericKey {
				t.Fatal("fixture did not change executable identity and generic list isolation")
			}
			if first.Binding != second.Binding || first.Path != second.Path {
				t.Fatalf("EXECUTABLE_METADATA_REBUILD: count=%d first=%s second=%s", count, first.Binding, second.Binding)
			}
			if first.Count != uint64(count) || second.Count != first.Count || second.Transaction != first.Transaction || second.Generation != first.Generation || first.Generation == "" || second.Checks > 32 {
				t.Fatalf("count=%d first=%+v second=%+v", count, first, second)
			}
			t.Logf("count=%d first_checks=%d second_checks=%d transaction=%d managed_generation=%s", count, first.Checks, second.Checks, second.Transaction, second.Generation)
		})
	}
}

func TestQueryMetadataSemanticGenerationIsolation(t *testing.T) {
	s := initStore(t)
	create(t, s, docOps("actual"))
	q, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []struct {
		name       string
		version    int
		generation string
	}{
		{name: "prior semantic generation", version: 4, generation: foldCacheGeneration},
		{name: "other operation vocabulary", version: queryMetadataVersion(model.KindDoc), generation: foldGeneration([]string{"older_op"})},
	} {
		t.Run(foreign.name, func(t *testing.T) {
			other := q
			other.binding = s.listCacheName(model.KindDoc, fmt.Appendf(nil, "metadata %d", foreign.version), foreign.generation, "")
			if other.binding == q.binding {
				t.Fatalf("foreign semantic generation shares binding %s", q.binding)
			}
			sentinel := []byte("foreign projection must not be read or changed")
			db, err := bolt.Open(q.indexPath(), 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				binding, err := ensureQueryMetadataBinding(tx, other.binding)
				if err != nil {
					return err
				}
				return binding.Bucket(queryMetadataRowsBucket).Put([]byte("foreign"), sentinel)
			})
			if closeErr := db.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				t.Fatal(err)
			}
			before := readQueryExecutableResult(t, s)
			if result := readQueryExecutableResult(t, s); result.Count != 1 || result.Generation != before.Generation || result.Transaction != before.Transaction {
				t.Fatalf("semantic query changed managed view: before=%+v result=%+v", before, result)
			}
			if err := s.managedView(t.Context(), func(tx *bolt.Tx) error {
				if data := readQueryMetadata(tx, other.binding, "foreign"); string(data) != string(sentinel) {
					t.Fatalf("foreign projection changed: %q", data)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
