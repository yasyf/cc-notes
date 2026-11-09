package gitguard

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

func processTable() (map[int]process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	table := make(map[int]process, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, err
		}
		fields := bytes.Fields(stat[bytes.LastIndexByte(stat, ')')+1:])
		parent, err := strconv.Atoi(string(fields[1]))
		if err != nil {
			return nil, fmt.Errorf("/proc/%d/stat: %w", pid, err)
		}
		state := string(fields[0])
		exited := state == "Z" || state == "X"
		table[pid] = process{parent: parent, halted: exited || state == "T" || state == "t", exited: exited}
	}
	return table, nil
}
