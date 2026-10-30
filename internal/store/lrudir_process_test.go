package store

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/homeguard"
)

const cacheProcessEnv = "CCN_LRU_PROCESS_REQUEST"

type cacheProcessRequest struct {
	Mode     string
	Dir      string
	Capacity int
	Name     string
	Data     string
	Names    []string
}

type cacheProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output io.ReadCloser
	stderr bytes.Buffer
	waited bool
}

func TestLRUDirProcessHelper(t *testing.T) {
	raw, ok := os.LookupEnv(cacheProcessEnv)
	if !ok {
		return
	}
	var request cacheProcessRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	input := bufio.NewReader(os.Stdin)
	if line, err := input.ReadString('\n'); err != nil || line != "go\n" {
		t.Fatalf("start barrier = %q, %v", line, err)
	}
	l := &lruDir{capacity: request.Capacity, dir: request.Dir}
	switch request.Mode {
	case "batch", "batch-commit":
		values := make([]cachePublication, len(request.Names))
		for i, name := range request.Names {
			values[i] = cachePublication{name: name, data: []byte(request.Data + name)}
		}
		l.writeBatch(values)
		if request.Mode == "batch-commit" {
			homeguard.ChildExit(77)
		}
	case "write":
		l.write(request.Name, []byte(request.Data))
	case "commit":
		l.write(request.Name, []byte(request.Data))
		if data, ok := l.read(request.Name); !ok || string(data) != request.Data {
			t.Fatalf("committed entry = %q, %v; want %q", data, ok, request.Data)
		}
	case "read":
		values := make(map[string]*string, len(request.Names))
		for _, name := range request.Names {
			values[name] = nil
			if data, ok := l.read(name); ok {
				value := string(data)
				values[name] = &value
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
			t.Fatal(err)
		}
	case "pending", "rename", "hold", "batch-rename":
		lock, ok := l.lockWriter()
		if !ok {
			t.Fatal("acquire publication lock")
		}
		defer func() { _ = lock.Close() }()
		db, err := l.database(cacheWrite)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if request.Mode == "hold" {
			var rank int64
			if err := tx.QueryRow("SELECT rank FROM cache_meta WHERE id = 1").Scan(&rank); err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
				t.Fatal(err)
			}
			if value, err := input.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("crash barrier = %q, %v; want EOF", value, err)
			}
			homeguard.ChildExit(77)
		}
		if request.Mode == "batch-rename" {
			var rank int64
			if err := tx.QueryRow("SELECT rank FROM cache_meta WHERE id = 1").Scan(&rank); err != nil {
				t.Fatal(err)
			}
			for _, name := range request.Names {
				slot, err := l.allocate(tx, name)
				if err != nil {
					t.Fatal(err)
				}
				var token [cacheTokenSize]byte
				key := sha256.Sum256([]byte(name))
				copy(token[:32], key[:])
				if _, err := rand.Read(token[32:]); err != nil {
					t.Fatal(err)
				}
				data := []byte(request.Data + name)
				if !l.publish(slot, token[:], data) {
					t.Fatal("batch prefix publication failed")
				}
				rank++
				if _, err := tx.Exec("INSERT INTO cache_entries VALUES (?, ?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET slot=excluded.slot, token=excluded.token, rank=excluded.rank, size=excluded.size", name, slot, token[:], rank, len(data)); err != nil {
					t.Fatal(err)
				}
			}
			homeguard.ChildExit(77)
		}
		slot, err := l.allocate(tx, request.Name)
		if err != nil {
			t.Fatal(err)
		}
		var token [cacheTokenSize]byte
		key := sha256.Sum256([]byte(request.Name))
		copy(token[:32], key[:])
		if _, err := rand.Read(token[32:]); err != nil {
			t.Fatal(err)
		}
		if request.Mode == "pending" {
			if err := os.WriteFile(filepath.Join(l.dir, ".pending"), append(token[:], request.Data...), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if !l.publish(slot, token[:], []byte(request.Data)) {
			t.Fatal("publish failed before crash")
		}
		homeguard.ChildExit(77)
	default:
		t.Fatalf("unknown helper mode %q", request.Mode)
	}
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	homeguard.ChildExit(0)
}

func startCacheProcess(t *testing.T, request cacheProcessRequest) *cacheProcess {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	p := &cacheProcess{cmd: exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLRUDirProcessHelper$", "-test.count=1")}
	p.cmd.Env = append(os.Environ(), cacheProcessEnv+"="+string(raw))
	p.cmd.Stderr = &p.stderr
	p.input, err = p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.output, err = p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !p.waited {
			_ = p.input.Close()
			_ = p.cmd.Wait()
		}
	})
	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(p.output, ready); err != nil || string(ready) != "ready\n" {
		_ = p.input.Close()
		_ = p.cmd.Wait()
		p.waited = true
		t.Fatalf("helper ready = %q, %v; stderr %q", ready, err, p.stderr.String())
	}
	return p
}

func (p *cacheProcess) release(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(p.input, "go\n"); err != nil {
		t.Fatal(err)
	}
	if err := p.input.Close(); err != nil {
		t.Fatal(err)
	}
}

func (p *cacheProcess) wait(t *testing.T, wantExit int) []byte {
	t.Helper()
	output, readErr := io.ReadAll(p.output)
	err := p.cmd.Wait()
	p.waited = true
	if readErr != nil {
		t.Fatal(readErr)
	}
	var exit *exec.ExitError
	if wantExit == 0 && err != nil || wantExit != 0 && (!errors.As(err, &exit) || exit.ExitCode() != wantExit) {
		t.Fatalf("helper exit = %v, want %d; stdout %q; stderr %q", err, wantExit, output, p.stderr.String())
	}
	if p.stderr.Len() != 0 {
		t.Fatalf("helper stderr = %q", p.stderr.String())
	}
	return output
}

func runCacheProcess(t *testing.T, request cacheProcessRequest, wantExit int) []byte {
	t.Helper()
	p := startCacheProcess(t, request)
	p.release(t)
	return p.wait(t, wantExit)
}

func requireCacheProcessValues(t *testing.T, l *lruDir, want map[string]*string) {
	t.Helper()
	names := slices.Sorted(maps.Keys(want))
	output := runCacheProcess(t, cacheProcessRequest{Mode: "read", Dir: l.dir, Capacity: l.capacity, Names: names}, 0)
	var got map[string]*string
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode helper output %q: %v", output, err)
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for name, value := range want {
		actual, present := got[name]
		if !present || value == nil && actual != nil || value != nil && (actual == nil || *actual != *value) {
			t.Fatalf("entry %q = %v, present=%v; want %v", name, actual, present, value)
		}
	}
}

func requireCacheSlotBound(t *testing.T, l *lruDir) {
	t.Helper()
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		t.Fatal(err)
	}
	slots, pending := 0, 0
	for _, entry := range entries {
		switch {
		case strings.HasPrefix(entry.Name(), "slot-"):
			if entry.IsDir() {
				t.Fatalf("slot %q is a directory", entry.Name())
			}
			slots++
		case entry.Name() == ".pending":
			pending++
		case entry.Name() == "index.sqlite", entry.Name() == "index.sqlite-wal", entry.Name() == "index.sqlite-shm":
		default:
			t.Fatalf("unexpected cache artifact %q", entry.Name())
		}
	}
	if slots > l.capacity || pending > 1 {
		t.Fatalf("payload files: slots=%d pending=%d, capacity=%d", slots, pending, l.capacity)
	}
}

func TestLRUDirProcessCrashPublication(t *testing.T) {
	for _, step := range []struct {
		mode string
		name string
	}{
		{"pending", "c"},
		{"rename", "c"},
		{"rename", "a"},
		{"commit", "c"},
	} {
		t.Run(step.mode+"_"+step.name, func(t *testing.T) {
			l := newProcessCache(t, 2)
			a, b, c := "payload:a", "payload:b", "payload:c"
			l.write("a", []byte(a))
			l.write("b", []byte(b))
			exit := 77
			if step.mode == "commit" {
				exit = 0
			}
			if output := runCacheProcess(t, cacheProcessRequest{Mode: step.mode, Dir: l.dir, Capacity: l.capacity, Name: step.name, Data: c}, exit); len(output) != 0 {
				t.Fatalf("unexpected helper output %q", output)
			}
			want := map[string]*string{"a": &a, "b": &b, "c": nil}
			if step.mode == "rename" || step.mode == "commit" {
				want["a"] = nil
			}
			if step.mode == "commit" {
				want["c"] = &c
			}
			requireCacheProcessValues(t, l, want)
			requireCacheSlotBound(t, l)
			d := "payload:d"
			runCacheProcess(t, cacheProcessRequest{Mode: "commit", Dir: l.dir, Capacity: l.capacity, Name: "d", Data: d}, 0)
			requireCacheProcessValues(t, l, map[string]*string{"d": &d})
			requireCacheSlotBound(t, l)
		})
	}
}

func TestLRUDirProcessBatchCrashAndGC(t *testing.T) {
	for _, mode := range []string{"batch-rename", "batch-commit"} {
		t.Run(mode, func(t *testing.T) {
			l := newProcessCache(t, 3)
			a, b, c := "old:a", "old:b", strings.Repeat("old:c", cacheChunkSize)
			l.writeBatch([]cachePublication{{"a", []byte(a)}, {"b", []byte(b)}, {"c", []byte(c)}})
			runCacheProcess(t, cacheProcessRequest{Mode: mode, Dir: l.dir, Capacity: l.capacity, Names: []string{"a", "b"}, Data: "new:"}, 77)
			want := map[string]*string{"a": nil, "b": nil, "c": &c}
			if mode == "batch-commit" {
				a, b = "new:a", "new:b"
				want["a"], want["b"] = &a, &b
			}
			requireCacheProcessValues(t, l, want)
			reader, ok := l.open("c")
			if !ok {
				t.Fatal("held batch stream missing")
			}
			defer func() { _ = reader.Close() }()
			l.remove("c")
			runCacheProcess(t, cacheProcessRequest{Mode: "batch", Dir: l.dir, Capacity: l.capacity, Names: []string{"d", "e"}, Data: "after:"}, 0)
			d, e := "after:d", "after:e"
			requireCacheProcessValues(t, l, map[string]*string{"d": &d, "e": &e})
			data, err := io.ReadAll(reader)
			if err != nil || string(data) != c {
				t.Fatalf("held batch stream after GC/reuse = %d, %v", len(data), err)
			}
			requireCacheSlotBound(t, l)
		})
	}
}

func TestLRUDirCompetingPublicationBatches(t *testing.T) {
	l := newProcessCache(t, 3)
	for _, name := range []string{"base-a", "base-b", "base-c"} {
		l.write(name, []byte("payload:"+name))
	}
	processes := make([]*cacheProcess, 0, 4)
	for i := range 4 {
		processes = append(processes, startCacheProcess(t, cacheProcessRequest{Mode: "batch", Dir: l.dir, Capacity: l.capacity, Names: []string{fmt.Sprintf("batch-%d-a", i), fmt.Sprintf("batch-%d-b", i)}, Data: "payload:"}))
	}
	for _, process := range processes {
		process.release(t)
	}
	for _, process := range processes {
		process.wait(t, 0)
	}
	if names := l.names(); len(names) != l.capacity {
		t.Fatalf("competing batch capacity = %v", names)
	} else {
		for _, name := range names {
			if data, ok := l.read(name); !ok || string(data) != "payload:"+name {
				t.Fatalf("competing batch value %s = %q, %v", name, data, ok)
			}
		}
	}
	requireCacheSlotBound(t, l)
}

func TestLRUDirProcessesShareCapacity(t *testing.T) {
	l := newProcessCache(t, 3)
	for _, name := range []string{"base-a", "base-b", "base-c"} {
		l.write(name, []byte("payload:"+name))
	}
	processes := make([]*cacheProcess, 6)
	for i := range processes {
		name := fmt.Sprintf("writer-%d", i)
		processes[i] = startCacheProcess(t, cacheProcessRequest{Mode: "write", Dir: l.dir, Capacity: l.capacity, Name: name, Data: "payload:" + name})
	}
	for _, process := range processes {
		process.release(t)
	}
	for _, process := range processes {
		if output := process.wait(t, 0); len(output) != 0 {
			t.Fatalf("unexpected helper output %q", output)
		}
	}
	names := l.names()
	if len(names) != l.capacity {
		t.Fatalf("entries = %v, want %d entries", names, l.capacity)
	}
	written := false
	want := make(map[string]*string, len(names))
	for _, name := range names {
		value := "payload:" + name
		want[name] = &value
		written = written || strings.HasPrefix(name, "writer-")
	}
	if !written {
		t.Fatalf("no concurrent writer committed: %v", names)
	}
	requireCacheProcessValues(t, l, want)
	requireCacheSlotBound(t, l)
}

func TestLRUDirOpenDescriptorSurvivesProcessPublication(t *testing.T) {
	l := newProcessCache(t, 1)
	l.write("a", []byte("old"))
	for _, step := range []struct {
		name string
		data string
		want string
	}{
		{"a", "new", "old"},
		{"b", "replacement", "new"},
	} {
		f, ok := l.open("a")
		if !ok {
			t.Fatal("open original entry")
		}
		runCacheProcess(t, cacheProcessRequest{Mode: "commit", Dir: l.dir, Capacity: l.capacity, Name: step.name, Data: step.data}, 0)
		data, err := io.ReadAll(f)
		closeErr := f.Close()
		if err != nil || closeErr != nil || string(data) != step.want {
			t.Fatalf("opened descriptor = %q, %v, %v; want %q", data, err, closeErr, step.want)
		}
	}
	want := "replacement"
	requireCacheProcessValues(t, l, map[string]*string{"a": nil, "b": &want})
	requireCacheSlotBound(t, l)
}

func TestLRUDirProcessPendingFailureDoesNotPublish(t *testing.T) {
	l := newProcessCache(t, 2)
	a, b := "payload:a", "payload:b"
	l.write("a", []byte(a))
	l.write("b", []byte(b))
	pending := filepath.Join(l.dir, ".pending")
	if err := os.Mkdir(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	runCacheProcess(t, cacheProcessRequest{Mode: "write", Dir: l.dir, Capacity: l.capacity, Name: "c", Data: "payload:c"}, 0)
	requireCacheProcessValues(t, l, map[string]*string{"a": &a, "b": &b, "c": nil})
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	c := "payload:c"
	runCacheProcess(t, cacheProcessRequest{Mode: "commit", Dir: l.dir, Capacity: l.capacity, Name: "c", Data: c}, 0)
	requireCacheProcessValues(t, l, map[string]*string{"a": nil, "b": &b, "c": &c})
	requireCacheSlotBound(t, l)
}

func TestLRUDirProcessWriterLockSurvivesMetadataReplacement(t *testing.T) {
	for _, mode := range []string{"directory", "index"} {
		t.Run(mode, func(t *testing.T) {
			l := newProcessCache(t, 2)
			l.write("old", []byte("old generation"))
			if err := l.close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(l.dir + ".lock")
			if err != nil {
				t.Fatal(err)
			}
			held := startCacheProcess(t, cacheProcessRequest{Mode: "hold", Dir: l.dir, Capacity: l.capacity})
			if _, err := io.WriteString(held.input, "go\n"); err != nil {
				t.Fatal(err)
			}
			locked := make([]byte, len("locked\n"))
			if _, err := io.ReadFull(held.output, locked); err != nil || string(locked) != "locked\n" {
				_ = held.input.Close()
				held.wait(t, 77)
				t.Fatalf("publication barrier = %q, %v", locked, err)
			}
			if mode == "directory" {
				if err := os.RemoveAll(l.dir); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(l.dir, 0o750); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, name := range []string{"index.sqlite", "index.sqlite-wal", "index.sqlite-shm"} {
					if err := os.Remove(filepath.Join(l.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
				}
			}
			after, err := os.Stat(l.dir + ".lock")
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("replacement changed coordination inode: %v", err)
			}
			pending := filepath.Join(l.dir, ".pending")
			staging := []byte("held generation staging remains untouched")
			if err := os.WriteFile(pending, staging, 0o600); err != nil {
				t.Fatal(err)
			}
			request := cacheProcessRequest{Mode: "write", Dir: l.dir, Capacity: l.capacity, Name: "new", Data: "new generation"}
			if output := runCacheProcess(t, request, 0); len(output) != 0 {
				t.Fatalf("unexpected blocked-writer output %q", output)
			}
			if _, err := os.Stat(filepath.Join(l.dir, "index.sqlite")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("blocked writer initialized replacement metadata: %v", err)
			}
			if data, err := os.ReadFile(pending); err != nil || !bytes.Equal(data, staging) {
				t.Fatalf("blocked writer changed pending payload: %q, %v", data, err)
			}
			if err := held.input.Close(); err != nil {
				t.Fatal(err)
			}
			if output := held.wait(t, 77); len(output) != 0 {
				t.Fatalf("unexpected crashed-writer output %q", output)
			}
			request.Mode = "commit"
			runCacheProcess(t, request, 0)
			want := request.Data
			requireCacheProcessValues(t, l, map[string]*string{"old": nil, "new": &want})
			requireCacheSlotBound(t, l)
		})
	}
}

func newProcessCache(t *testing.T, capacity int) *lruDir {
	t.Helper()
	l := &lruDir{capacity: capacity, dir: t.TempDir()}
	t.Cleanup(func() {
		if err := l.close(); err != nil {
			t.Error(err)
		}
	})
	return l
}
