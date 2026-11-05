package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/store"
)

const authorityImportProgress = 10 * time.Second

// ErrAuthorityImporting reports a first import that outlasted the caller's
// managed.LockWaitEnv; it finishes in the background and a later command proceeds.
var ErrAuthorityImporting = errors.New("importing existing records into the managed authority in the background; retry shortly")

func ensureAuthority(ctx context.Context, s *store.Store, progress io.Writer) (err error) {
	if s.AuthorityExists() {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	logPath, err := s.AuthorityImportLog()
	if err != nil {
		return fmt.Errorf("import managed authority: %w", err)
	}
	log, err := os.Create(logPath) //nolint:gosec // G304: The log path is a fixed child of the records common directory.
	if err != nil {
		return fmt.Errorf("import managed authority: %w", err)
	}
	defer func() { err = errors.Join(err, log.Close()) }()
	// Hooks kill their command at a deadline, so an import in the command's own
	// session would never finish in a repository large enough to outlast it.
	//nolint:gosec,noctx // G204: re-executes this binary; the import outlives ctx by design.
	importer := exec.Command(exe, "reindex", "--if-missing", "--repo="+s.Git.Dir)
	importer.Stdout, importer.Stderr = log, log
	importer.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := importer.Start(); err != nil {
		return fmt.Errorf("import managed authority: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- importer.Wait() }()
	wait, err := managed.Wait(managed.Unbounded)
	if err != nil {
		return err
	}
	deadline := time.After(wait)
	started := time.Now()
	ticks := time.NewTicker(authorityImportProgress)
	defer ticks.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return ErrAuthorityImporting
		case err := <-exited:
			if err != nil {
				output, _ := os.ReadFile(logPath) //nolint:gosec // G304: The log path is a fixed child of the records common directory.
				return fmt.Errorf("import managed authority: %w: %s", err, strings.TrimSpace(string(output)))
			}
			return nil
		case <-ticks.C:
			if _, err := fmt.Fprintf(progress, "importing this clone's existing records into the managed authority, a one-time step: %s so far; the command runs when it finishes\n", time.Since(started).Round(time.Second)); err != nil {
				return err
			}
		}
	}
}
