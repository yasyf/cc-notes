package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const managedProcessEnv = "CCN_MANAGED_PROCESS_REQUEST"

type managedProcessRequest struct {
	Mode           string
	Dir            string
	Ref            string
	Tag            string
	Updates        []gitcmd.RefUpdate
	Before         []model.SHA
	Socket         string
	ExpectedIntent []byte
}

type managedProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output io.ReadCloser
	stderr bytes.Buffer
	waited bool
}

func TestManagedAuthorityProcessHelper(t *testing.T) {
	raw := os.Getenv(managedProcessEnv)
	if raw == "" {
		return
	}
	var request managedProcessRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	s, err := OpenContext(t.Context(), request.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	input := bufio.NewReader(os.Stdin)
	if line, err := input.ReadString('\n'); err != nil || line != "go\n" {
		t.Fatalf("start barrier=%q, %v", line, err)
	}
	switch request.Mode {
	case "append":
		if _, err := s.Append(t.Context(), request.Ref, []model.Op{model.AddTag{Tag: request.Tag}}); err != nil {
			t.Fatal(err)
		}
	case "publish":
		go func() {
			if err := s.PublishRefs(t.Context(), request.Updates); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}()
		if line, err := input.ReadString('\n'); err != nil || line != "exit\n" {
			t.Fatalf("exit barrier=%q, %v", line, err)
		}
		homeguard.ChildExit(77)
	case "journal-before", "journal-after":
		rows, err := s.managedRows(t.Context(), request.Updates, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := managed.Lock(t.Context(), s.recordsCommonDir, true)
		if err != nil {
			t.Fatal(err)
		}
		db, err := s.openManaged(false)
		if err != nil {
			t.Fatal(err)
		}
		intent := managedIntent{Updates: request.Updates, Before: request.Before, Rows: rows}
		data, err := json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx *bolt.Tx) error {
			root, err := managedRoot(tx)
			if err != nil {
				return err
			}
			return root.Put(managedPendingKey, data)
		}); err != nil {
			t.Fatal(err)
		}
		if request.Mode == "journal-after" {
			if err := s.RecordsGit.Holding(lock).UpdateRefs(t.Context(), request.Updates); err != nil {
				t.Fatal(err)
			}
		}
		homeguard.ChildExit(77)
	default:
		t.Fatalf("unknown managed process mode %q", request.Mode)
	}
}

func startManagedProcess(t *testing.T, request managedProcessRequest) *managedProcess {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	p := &managedProcess{cmd: exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedAuthorityProcessHelper$", "-test.count=1")}
	p.cmd.Env = append(os.Environ(), managedProcessEnv+"="+string(raw))
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
		t.Fatalf("managed helper ready=%q, %v; stderr=%q", ready, err, p.stderr.String())
	}
	return p
}

func (p *managedProcess) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(p.input, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (p *managedProcess) wait(t *testing.T, wantExit int) {
	t.Helper()
	output, readErr := io.ReadAll(p.output)
	err := p.cmd.Wait()
	p.waited = true
	_ = p.input.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	var exit *exec.ExitError
	if wantExit == 0 && err != nil || wantExit != 0 && (!errors.As(err, &exit) || exit.ExitCode() != wantExit) {
		t.Fatalf("managed helper exit=%v; want %d; stdout=%q stderr=%q", err, wantExit, output, p.stderr.String())
	}
	if p.stderr.Len() != 0 {
		t.Fatalf("managed helper stderr=%q", p.stderr.String())
	}
}

func readManagedPending(t *testing.T, s *Store) managedIntent {
	t.Helper()
	db, err := bolt.Open(managed.Path(s.recordsCommonDir), 0o600, &bolt.Options{ReadOnly: true, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var intent managedIntent
	err = db.View(func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		return json.Unmarshal(root.Get(managedPendingKey), &intent)
	})
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	return intent
}

func TestManagedAuthorityProcessCrashRecoversOnlyPendingRefs(t *testing.T) {
	for _, mode := range []string{"journal-before", "journal-after"} {
		t.Run(mode, func(t *testing.T) {
			s := initStore(t)
			a := create(t, s, answerOps("crash recovery")).(model.Answer)
			ref := refs.For(model.KindAnswer, a.ID)
			prepared, err := s.PrepareAppendAt(t.Context(), ref, a.Head, []model.Op{model.AddTag{Tag: "recovered"}})
			if err != nil {
				t.Fatal(err)
			}
			poisonManagedUnrelatedRef(t, s, model.KindAnswer)
			updates := []gitcmd.RefUpdate{prepared.RefUpdate()}
			p := startManagedProcess(t, managedProcessRequest{Mode: mode, Dir: s.Git.Dir, Updates: updates, Before: []model.SHA{a.Head}})
			p.send(t, "go")
			p.wait(t, 77)
			intent := readManagedPending(t, s)
			if len(intent.Updates) != 1 || intent.Updates[0] != updates[0] || len(intent.Before) != 1 || intent.Before[0] != a.Head || len(intent.Rows) != 1 || intent.Rows[0].Tip != prepared.New {
				t.Fatalf("durable pending intent=%+v", intent)
			}
			want := model.Snapshot(a)
			if mode == "journal-after" {
				want = prepared.Snapshot
			}
			if tip, err := s.managedTip(t.Context(), ref); err != nil || tip != want.Meta().Head {
				t.Fatalf("recovered tip=%s, %v; want %s", tip, err, want.Meta().Head)
			}
			requireManagedProjection(t, s, ref, want)
			if mode == "journal-after" {
				requireManagedAnswerLabel(t, s, "recovered", a.ID)
			} else {
				requireManagedAnswerLabel(t, s, "recovered")
			}
		})
	}
}

func TestManagedAuthorityReferenceTransactionHook(t *testing.T) {
	raw := os.Getenv(managedProcessEnv)
	if raw == "" {
		return
	}
	var request managedProcessRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("unix", request.Socket, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, readErr := os.ReadFile(managed.Path(filepath.Join(request.Dir, ".git")))
	report := struct {
		Pending bool
		Error   string
	}{Pending: readErr == nil && bytes.Contains(data, request.ExpectedIntent)}
	if readErr != nil {
		report.Error = readErr.Error()
	}
	if err := json.NewEncoder(conn).Encode(report); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(conn).ReadString('\n'); err != nil || line != "continue\n" {
		t.Fatalf("Git hook barrier=%q, %v", line, err)
	}
	if !report.Pending {
		t.Fatalf("Git CAS entered before durable expected intent: %s", report.Error)
	}
}

func TestManagedAuthorityGuardianRetainsLockThroughCallerExit(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("guardian custody")).(model.Answer)
	ref := refs.For(model.KindAnswer, a.ID)
	prepared, err := s.PrepareAppendAt(t.Context(), ref, a.Head, []model.Op{model.AddTag{Tag: "surviving Git"}})
	if err != nil {
		t.Fatal(err)
	}
	poisonManagedUnrelatedRef(t, s, model.KindAnswer)
	updates := []gitcmd.RefUpdate{prepared.RefUpdate()}
	rows, err := s.managedRows(t.Context(), updates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := json.Marshal(managedIntent{Updates: updates, Before: []model.SHA{a.Head}, Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("", "ccn-ma-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "hook")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }
	hook := "#!/bin/sh\n[ \"$1\" = prepared ] || exit 0\nexec " + quote(os.Args[0]) + " '-test.run=^TestManagedAuthorityReferenceTransactionHook$' '-test.count=1'\n"
	if err := os.WriteFile(filepath.Join(s.recordsCommonDir, "hooks", "reference-transaction"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	p := startManagedProcess(t, managedProcessRequest{Mode: "publish", Dir: s.Git.Dir, Updates: updates, Socket: socket, ExpectedIntent: intent})
	p.send(t, "go")
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = io.WriteString(conn, "continue\n"); _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Pending bool
		Error   string
	}
	if err := json.NewDecoder(conn).Decode(&report); err != nil || !report.Pending {
		t.Fatalf("prepared Git hook durable intent=%+v, %v", report, err)
	}
	p.send(t, "exit")
	p.wait(t, 77)
	pending := readManagedPending(t, s)
	if len(pending.Updates) != 1 || pending.Updates[0] != updates[0] {
		t.Fatalf("caller exit discarded durable intent=%+v", pending)
	}
	lock, err := os.OpenFile(filepath.Join(s.recordsCommonDir, "cc-notes-managed", "authority-v1.db.lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	lockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if lockErr == nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(lockErr, syscall.EWOULDBLOCK) {
		t.Fatalf("stable lock while surviving Git is prepared=%v; want held", lockErr)
	}
	if tip, err := s.Repo.Tip(t.Context(), ref); err != nil || tip != a.Head {
		t.Fatalf("prepared transaction already moved Git=%s, %v", tip, err)
	}
	if _, err := io.WriteString(conn, "continue\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if tip, err := s.managedTip(t.Context(), ref); err != nil || tip != prepared.New {
		t.Fatalf("surviving Git recovery=%s, %v; want %s", tip, err, prepared.New)
	}
	requireManagedProjection(t, s, ref, prepared.Snapshot)
	requireManagedAnswerLabel(t, s, "surviving Git", a.ID)
}

func TestManagedAuthorityConcurrentWriterProcesses(t *testing.T) {
	for _, sameRef := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_ref=%t", sameRef), func(t *testing.T) {
			s := initStore(t)
			a := create(t, s, answerOps("concurrent first")).(model.Answer)
			b := a
			if !sameRef {
				b = create(t, s, answerOps("concurrent second")).(model.Answer)
			}
			first := startManagedProcess(t, managedProcessRequest{Mode: "append", Dir: s.Git.Dir, Ref: refs.For(model.KindAnswer, a.ID), Tag: "first writer"})
			second := startManagedProcess(t, managedProcessRequest{Mode: "append", Dir: s.Git.Dir, Ref: refs.For(model.KindAnswer, b.ID), Tag: "second writer"})
			first.send(t, "go")
			second.send(t, "go")
			first.wait(t, 0)
			second.wait(t, 0)
			for _, id := range []model.EntityID{a.ID, b.ID} {
				ref := refs.For(model.KindAnswer, id)
				loaded, err := s.Load(t.Context(), ref)
				if err != nil {
					t.Fatal(err)
				}
				requireManagedProjection(t, s, ref, loaded)
			}
			requireManagedAnswerLabel(t, s, "first writer", a.ID)
			requireManagedAnswerLabel(t, s, "second writer", b.ID)
		})
	}
}
