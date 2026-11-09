package gitguard

import "golang.org/x/sys/unix"

const (
	darwinStopped = 4
	darwinZombie  = 5
)

func processTable() (map[int]process, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	table := make(map[int]process, len(procs))
	for _, p := range procs {
		exited := p.Proc.P_stat == darwinZombie
		table[int(p.Proc.P_pid)] = process{
			parent: int(p.Eproc.Ppid),
			halted: exited || p.Proc.P_stat == darwinStopped,
			exited: exited,
		}
	}
	return table, nil
}
