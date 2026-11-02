package store

import (
	"reflect"
	"testing"
)

func TestManagedCostTraceAccountsOperationAndDescendantWork(t *testing.T) {
	raw := []byte(`1700000000.000001 read(3, "outside", 7) = 7
1700000000.000002 write(2, "CCN_MANAGED_COST_BEGIN\n", 23) = 23
1700000000.000003 read(3, "abc", 3) = 3
1700000000.000004 read(3, <unfinished ...>
1700000000.000005 --- SIGURG {si_signo=SIGURG, si_code=SI_TKILL} ---
1700000000.000006 <... read resumed>"defgh", 5) = 5
1700000000.000007 writev(4, [{iov_base="abcdefg", iov_len=7}], 1) = 7
1700000000.000008 mmap(NULL, 4096, PROT_READ, MAP_SHARED, 8, 0) = 0x7fa000
1700000000.000009 mmap(NULL, 8192, PROT_READ|PROT_WRITE, MAP_ANONYMOUS|MAP_PRIVATE, -1, 0) = 0x7fb000
1700000000.000010 openat(AT_FDCWD, "missing", O_RDONLY) = -1 ENOENT (No such file or directory)
1700000000.000011 read(3, 0x4, 5) = ? ERESTARTSYS (To be restarted if SA_RESTART is set)
1700000000.000020 write(2, "CCN_MANAGED_COST_END\n", 21) = 21
1700000000.000021 write(1, "outside", 7) = 7
1700000000.000022 +++ exited with 0 +++
`)
	child := []byte(`1700000000.000012 execve("/usr/bin/git", ["git", "rev-parse"], 0x4) = 0
1700000000.000013 pread64(3, "child", 5, 0) = 5
1700000000.000014 execve("/missing/git", ["git"], 0x4) = -1 ENOENT (No such file or directory)
`)
	calls, err := managedCostParseTrace(raw)
	if err != nil {
		t.Fatal(err)
	}
	children, err := managedCostParseTrace(child)
	if err != nil {
		t.Fatal(err)
	}
	var result managedCostResult
	if err := managedCostAccountTrace(&result, append(calls, children...)); err != nil {
		t.Fatal(err)
	}
	wantCalls := map[string]int64{"read": 3, "pread64": 1, "writev": 1, "mmap": 2, "openat": 1, "execve": 2}
	if result.ReadBytes != 13 || result.WriteBytes != 7 || result.FileMapBytes != 4096 || result.GitProcesses != 1 || !reflect.DeepEqual(result.Syscalls, wantCalls) {
		t.Fatalf("trace totals=%+v; want read=13 write=7 mapped=4096 git=1 syscalls=%v", result, wantCalls)
	}
}

func TestManagedCostTraceRejectsUnaccountedEvidence(t *testing.T) {
	for _, raw := range []string{
		"1793615657.374343 ???( <detached ...>\n",
		"1700000000.000001 read(3, \"unfinished\", 10 <detached ...>\n",
		"1700000000.000001 unreadable syscall\n",
		"1700000000.000001 <... read resumed>0) = 0\n",
		"1700000000.000001 read(3, <unfinished ...>\n",
		"1700000000.000001 read(3, <unfinished ...>\n1700000000.000002 <... write resumed>0) = 0\n",
	} {
		if _, err := managedCostParseTrace([]byte(raw)); err == nil {
			t.Errorf("accepted incomplete trace %q", raw)
		}
	}
	if err := managedCostAccountTrace(&managedCostResult{}, nil); err == nil {
		t.Error("accepted absent operation markers")
	}
}
