package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
)

var errBindingPresent = errors.New("binding already present")

// BindResult reports one Bind: the context's common directory, the binding it
// now carries, and whether this call published it.
type BindResult struct {
	Context string
	Binding Binding
	Changed bool
}

// Bind publishes contextDir's storage binding to the repository containing
// sourceDir. It writes only cc-notes.storage into the context's common config
// file, through git's own lockfile protocol, and reads the source without
// modifying it. A source that is itself bound contributes its own backend, so
// the published binding is always direct: bindings never chain. The backend
// is validated before an identical existing binding is accepted. A record that
// lands in the context while the binding is being published is reported as
// ErrContextHasRecords naming every ref the binding hides; the binding stays,
// since nothing rewrites the context config after publication.
func Bind(ctx context.Context, contextDir, sourceDir string) (BindResult, error) {
	_, ctxCommon, _, err := gitcmd.Git{Dir: contextDir}.Dirs(ctx)
	if err != nil {
		return BindResult{}, fmt.Errorf("open context repository at %s: %w", contextDir, err)
	}
	config := filepath.Join(ctxCommon, "config")
	info, err := os.Lstat(config)
	if err != nil {
		return BindResult{}, &BindingError{Config: config, Err: fmt.Errorf("%w: %w", ErrBindingMalformed, err)}
	}
	if !info.Mode().IsRegular() {
		return BindResult{}, &BindingError{Config: config, Err: fmt.Errorf("%w: %s is not a regular file", ErrBindingMalformed, config)}
	}
	want, err := resolveSource(ctx, sourceDir)
	if err != nil {
		return BindResult{}, err
	}
	fail := func(err error) error { return &BindingError{Config: config, Source: want.CommonDir, Err: err} }
	contextID, err := fileIDOf(ctxCommon)
	if err != nil {
		return BindResult{}, fmt.Errorf("stat context common directory %s: %w", ctxCommon, err)
	}
	if want.id() == contextID {
		return BindResult{}, fail(fmt.Errorf("%w: %s is the context repository", ErrBindingCycle, want.CommonDir))
	}
	if _, err := validateBackend(want, contextID); err != nil {
		return BindResult{}, fail(err)
	}
	if _, err := openRecords(want); err != nil {
		return BindResult{}, fail(err)
	}
	absent := func(path string) error {
		existing, bound, err := readBinding(path)
		if err != nil {
			return &BindingError{Config: config, Err: err}
		}
		switch {
		case !bound:
			return nil
		case existing == want:
			return errBindingPresent
		default:
			return &BindingError{Config: config, Source: existing.CommonDir, Err: fmt.Errorf("%w: bound to %s, asked to bind %s", ErrBindingConflict, existing.CommonDir, want.CommonDir)}
		}
	}
	unchanged := BindResult{Context: ctxCommon, Binding: want}
	switch err := absent(config); {
	case errors.Is(err, errBindingPresent):
		return unchanged, nil
	case err != nil:
		return BindResult{}, err
	}
	records := gitcmd.Backend(ctxCommon)
	held, err := records.FirstRef(ctx, refs.Namespace)
	if err != nil {
		return BindResult{}, fmt.Errorf("probe context records: %w", err)
	}
	if held != "" {
		return BindResult{}, fail(fmt.Errorf("%w: %s", ErrContextHasRecords, held))
	}
	publish := func(lock string) error {
		if err := absent(lock); err != nil {
			return err
		}
		if err := records.ConfigFileSet(ctx, lock, bindingKey, want.String()); err != nil {
			return fmt.Errorf("publish binding: %w", err)
		}
		published, bound, err := readBinding(lock)
		if err != nil {
			return fmt.Errorf("publish binding: wrote %s, read back failed: %w", want, err)
		}
		if !bound || published != want {
			return fmt.Errorf("publish binding: wrote %s, read back %+v (bound %v)", want, published, bound)
		}
		return nil
	}
	switch err := replaceConfig(config, info.Mode().Perm(), publish); {
	case errors.Is(err, errBindingPresent):
		return unchanged, nil
	case err != nil:
		return BindResult{}, err
	}
	hidden, err := records.RefEntries(ctx, refs.Namespace)
	if err != nil {
		return BindResult{}, fmt.Errorf("probe context records after publishing: %w", err)
	}
	if len(hidden) == 0 {
		return BindResult{Context: ctxCommon, Binding: want, Changed: true}, nil
	}
	names := make([]string, len(hidden))
	for i, entry := range hidden {
		names[i] = entry.Ref
	}
	return BindResult{}, fail(fmt.Errorf("%w: binding to %s was published and stays; it hides %s", ErrContextHasRecords, want.CommonDir, strings.Join(names, ", ")))
}

// replaceConfig publishes a new version of config through git's own lockfile
// protocol: config.lock is created exclusively with perm, seeded with the
// current bytes without following a symlink, handed to edit, and renamed over
// config. A concurrent git config writer or binder fails on the lock instead
// of racing; an existing lock is refused, never waited on or removed.
func replaceConfig(config string, perm fs.FileMode, edit func(lock string) error) error {
	lock := config + ".lock"
	//nolint:gosec // G304: lock is the context's own config.lock under its git common directory.
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s exists: another process is writing the context config; retry once it finishes", lock)
	}
	if err != nil {
		return err
	}
	if err := seedLock(f, config); err != nil {
		_ = f.Close()
		_ = os.Remove(lock)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(lock)
		return err
	}
	if err := edit(lock); err != nil {
		_ = os.Remove(lock)
		return err
	}
	return os.Rename(lock, config)
}

func seedLock(lock *os.File, config string) error {
	//nolint:gosec // G304: config is the context's own config file under its git common directory.
	current, err := os.OpenFile(config, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	_, err = io.Copy(lock, current)
	return err
}

// resolveSource turns any path inside the source repository into the binding
// to publish: the source's canonical common directory with its current
// identity, or, when the source is itself bound, the binding it carries.
func resolveSource(ctx context.Context, sourceDir string) (Binding, error) {
	_, srcCommon, _, err := gitcmd.Discover(ctx, sourceDir)
	if err != nil {
		return Binding{}, fmt.Errorf("open source repository at %s: %w", sourceDir, err)
	}
	srcCanon, err := filepath.EvalSymlinks(srcCommon)
	if err != nil {
		return Binding{}, fmt.Errorf("resolve source %s: %w", srcCommon, err)
	}
	srcConfig := filepath.Join(srcCanon, "config")
	target, bound, err := readBinding(srcConfig)
	if err != nil {
		return Binding{}, &BindingError{Config: srcConfig, Err: err}
	}
	if bound {
		return target, nil
	}
	id, err := fileIDOf(srcCanon)
	if err != nil {
		return Binding{}, fmt.Errorf("stat source %s: %w", srcCanon, err)
	}
	return Binding{CommonDir: srcCanon, Device: id.device, Inode: id.inode}, nil
}
