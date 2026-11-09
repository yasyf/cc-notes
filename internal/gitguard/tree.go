package gitguard

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"syscall"
	"time"
)

// haltLimit bounds the wait for a process in uninterruptible sleep, which
// takes a stop signal only once it wakes; the kill goes ahead at the limit.
const haltLimit = time.Second

type process struct {
	parent         int
	halted, exited bool
}

// KillTree kills root and every process descended from it, such as a push's
// remote helper, send-pack, pack-objects, and their lazy fetches. It stops the
// tree until a scan sees every member stopped, so none can start a child the
// kill misses, then kills children before parents, so no pid it signals has
// been reaped and handed to an unrelated process.
//
// The tree stays in its caller's process group: a group of its own would
// escape a supervisor that kills the caller's group.
func KillTree(root *os.Process) error {
	if err := root.Signal(syscall.SIGSTOP); err != nil {
		return err
	}
	tree, err := haltTree(root.Pid)
	slices.Reverse(tree)
	for _, pid := range tree {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return errors.Join(err, root.Kill())
}

// haltTree stops every descendant of root and returns them, each after its
// parent, once one scan sees root and all of them stopped or exited, or once
// root itself has exited.
func haltTree(root int) ([]int, error) {
	var tree []int
	signaled := map[int]bool{root: true}
	for deadline := time.Now().Add(haltLimit); ; {
		table, err := processTable()
		if err != nil {
			return tree, err
		}
		if p, ok := table[root]; !ok || p.exited {
			return tree, nil
		}
		children := make(map[int][]int, len(table))
		for pid, p := range table {
			children[p.parent] = append(children[p.parent], pid)
		}
		settled := table[root].halted
		for generation := []int{root}; len(generation) > 0; {
			var next []int
			for _, parent := range generation {
				for _, pid := range children[parent] {
					if !signaled[pid] {
						_ = syscall.Kill(pid, syscall.SIGSTOP)
						signaled[pid] = true
						tree = append(tree, pid)
					}
					settled = settled && table[pid].halted
					next = append(next, pid)
				}
			}
			generation = next
		}
		if settled {
			return tree, nil
		}
		if time.Now().After(deadline) {
			return tree, fmt.Errorf("process tree of %d not stopped within %s", root, haltLimit)
		}
	}
}
