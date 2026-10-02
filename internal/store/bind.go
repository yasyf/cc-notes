package store

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
)

// BindResult reports one Bind: the context's common directory, the binding it
// now carries, and whether this call published it.
type BindResult struct {
	Context string
	Binding Binding
	Changed bool
}

// Bind publishes contextDir's storage binding to the repository containing
// sourceDir. It writes only cc-notes.storage into the context's common config
// file and reads the source without modifying it. A source that is itself
// bound contributes its own backend, so the published binding is always
// direct: bindings never chain.
func Bind(ctx context.Context, contextDir, sourceDir string) (BindResult, error) {
	_, ctxCommon, _, err := gitcmd.Git{Dir: contextDir}.Dirs(ctx)
	if err != nil {
		return BindResult{}, fmt.Errorf("open context repository at %s: %w", contextDir, err)
	}
	config := filepath.Join(ctxCommon, "config")
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
	existing, bound, err := readBinding(config)
	if err != nil {
		return BindResult{}, &BindingError{Config: config, Err: err}
	}
	if bound {
		if existing == want {
			return BindResult{Context: ctxCommon, Binding: want}, nil
		}
		return BindResult{}, &BindingError{Config: config, Source: existing.CommonDir, Err: fmt.Errorf("%w: bound to %s, asked to bind %s", ErrBindingConflict, existing.CommonDir, want.CommonDir)}
	}
	if err := validateBackend(want, contextID); err != nil {
		return BindResult{}, fail(err)
	}
	if _, err := openRecords(want); err != nil {
		return BindResult{}, fail(err)
	}
	records := gitcmd.Backend(ctxCommon)
	held, err := records.FirstRef(ctx, refs.Namespace)
	if err != nil {
		return BindResult{}, fmt.Errorf("probe context records: %w", err)
	}
	if held != "" {
		return BindResult{}, fail(fmt.Errorf("%w: %s", ErrContextHasRecords, held))
	}
	if err := records.ConfigFileSet(ctx, config, bindingKey, want.String()); err != nil {
		return BindResult{}, fmt.Errorf("publish binding: %w", err)
	}
	published, bound, err := readBinding(config)
	if err != nil {
		return BindResult{}, &BindingError{Config: config, Err: err}
	}
	if !bound || published != want {
		return BindResult{}, &BindingError{Config: config, Source: published.CommonDir, Err: fmt.Errorf("%w: a concurrent bind published %s", ErrBindingConflict, published.CommonDir)}
	}
	return BindResult{Context: ctxCommon, Binding: want, Changed: true}, nil
}

// resolveSource turns any path inside the source repository into the binding
// to publish: the source's canonical common directory with its current
// identity, or, when the source is itself bound, the binding it carries.
func resolveSource(ctx context.Context, sourceDir string) (Binding, error) {
	_, srcCommon, _, err := gitcmd.Backend(sourceDir).Dirs(ctx)
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
