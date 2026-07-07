package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Marker is the liveness record a running MCP server writes under the git common
// dir, so other tooling can see an in-process server holds the repo.
type Marker struct {
	PID       int   `json:"pid"`
	StartedAt int64 `json:"started_at"`
}

func markerPath(dir string, pid int) string {
	return filepath.Join(dir, fmt.Sprintf("%d.json", pid))
}

// WriteMarker sweeps dead-pid markers, then records this process's marker under
// dir, creating dir if needed.
func WriteMarker(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create mcp marker dir: %w", err)
	}
	sweepStale(dir)
	data, err := json.Marshal(Marker{PID: os.Getpid(), StartedAt: time.Now().Unix()})
	if err != nil {
		return err
	}
	if err := os.WriteFile(markerPath(dir, os.Getpid()), data, 0o600); err != nil {
		return fmt.Errorf("write mcp marker: %w", err)
	}
	return nil
}

// RemoveMarker deletes this process's marker.
func RemoveMarker(dir string) {
	_ = os.Remove(markerPath(dir, os.Getpid()))
}

// sweepStale removes marker files whose recorded pid is no longer alive, plus
// any unparseable marker.
func sweepStale(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		//nolint:gosec // G304: path is our own marker file under the git common dir, not external input.
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m Marker
		if err := json.Unmarshal(data, &m); err != nil || m.PID == 0 {
			_ = os.Remove(path)
			continue
		}
		if !pidAlive(m.PID) {
			_ = os.Remove(path)
		}
	}
}

// pidAlive reports whether a process with pid exists, via a signal-0 probe. A
// permission error means the process is alive but owned by another user.
func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
