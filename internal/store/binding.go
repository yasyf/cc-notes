package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yasyf/cc-notes/internal/gitobj"
)

const (
	bindingKey     = "cc-notes.storage"
	bindingVersion = 1
)

var (
	// ErrBindingMalformed reports a cc-notes.storage value that does not parse,
	// or a config holding more than one.
	ErrBindingMalformed = errors.New("malformed storage binding")
	// ErrBackendUnavailable reports a bound backend that is missing, unreadable,
	// not a git common directory, or of a layout cc-notes cannot read.
	ErrBackendUnavailable = errors.New("storage backend unavailable")
	// ErrBackendReplaced reports a backend whose device/inode no longer matches
	// the identity recorded when it was bound.
	ErrBackendReplaced = errors.New("storage backend identity changed")
	// ErrBackendRedirects reports a backend that carries a storage binding of
	// its own; bindings never chain.
	ErrBackendRedirects = errors.New("storage backend is itself bound")
	// ErrBindingCycle reports a binding that points a context at itself.
	ErrBindingCycle = errors.New("storage binding cycle")
	// ErrBindingChanged reports a context whose binding was published, removed,
	// or retargeted after the store opened; the caller reopens.
	ErrBindingChanged = errors.New("storage binding changed since open")
	// ErrBindingConflict reports a Bind against a context already bound to a
	// different backend.
	ErrBindingConflict = errors.New("context bound to a different backend")
	// ErrContextHasRecords reports a Bind against a context that already holds
	// cc-notes refs of its own, which the binding would hide.
	ErrContextHasRecords = errors.New("context already holds cc-notes records")
)

// Binding is the published pointer from a context checkout to the records
// repository it shares: the backend's canonical absolute common directory and
// the device/inode identity that directory had when it was bound.
type Binding struct {
	CommonDir string
	Device    uint64
	Inode     uint64
}

type bindingWire struct {
	Version   int    `json:"version"`
	CommonDir string `json:"commonDir"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
}

// String returns the canonical config value:
// {"version":1,"commonDir":"<abs>","device":<n>,"inode":<n>}
func (b Binding) String() string {
	data, err := json.Marshal(bindingWire{Version: bindingVersion, CommonDir: b.CommonDir, Device: b.Device, Inode: b.Inode})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func (b Binding) id() fileID { return fileID{device: b.Device, inode: b.Inode} }

// BindingError names the binding (the context config file holding it) and its
// source (the bound backend's common directory, "" when the value did not
// parse). It unwraps to exactly one of the sentinels above.
type BindingError struct {
	Config string
	Source string
	Err    error
}

func (e *BindingError) Error() string {
	if e.Source == "" {
		return fmt.Sprintf("storage binding %s in %s: %v", bindingKey, e.Config, e.Err)
	}
	return fmt.Sprintf("storage binding %s in %s (source %s): %v", bindingKey, e.Config, e.Source, e.Err)
}

func (e *BindingError) Unwrap() error { return e.Err }

type fileID struct {
	device, inode uint64
}

func fileIDOf(path string) (fileID, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileID{}, err
	}
	device, inode := gitobj.FileID(info)
	return fileID{device: device, inode: inode}, nil
}

func parseBinding(raw string) (Binding, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire bindingWire
	if err := dec.Decode(&wire); err != nil {
		return Binding{}, fmt.Errorf("%w: %v", ErrBindingMalformed, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Binding{}, fmt.Errorf("%w: trailing data after the binding object", ErrBindingMalformed)
	}
	switch {
	case wire.Version != bindingVersion:
		return Binding{}, fmt.Errorf("%w: version %d, want %d", ErrBindingMalformed, wire.Version, bindingVersion)
	case !filepath.IsAbs(wire.CommonDir):
		return Binding{}, fmt.Errorf("%w: commonDir %q is not absolute", ErrBindingMalformed, wire.CommonDir)
	case filepath.Clean(wire.CommonDir) != wire.CommonDir:
		return Binding{}, fmt.Errorf("%w: commonDir %q is not clean", ErrBindingMalformed, wire.CommonDir)
	case wire.Inode == 0:
		return Binding{}, fmt.Errorf("%w: inode is zero", ErrBindingMalformed)
	}
	return Binding{CommonDir: wire.CommonDir, Device: wire.Device, Inode: wire.Inode}, nil
}

// readBinding decodes exactly the config file at configPath. Zero values is
// unbound, one is parsed, more than one is malformed.
func readBinding(configPath string) (Binding, bool, error) {
	section, key, _ := strings.Cut(bindingKey, ".")
	values, err := gitobj.ConfigValues(configPath, section, key)
	if err != nil {
		return Binding{}, false, fmt.Errorf("%w: %v", ErrBindingMalformed, err)
	}
	switch len(values) {
	case 0:
		return Binding{}, false, nil
	case 1:
		b, err := parseBinding(values[0])
		return b, err == nil, err
	default:
		return Binding{}, false, fmt.Errorf("%w: %d values", ErrBindingMalformed, len(values))
	}
}

// validateBackend proves the bound backend is still the repository that was
// bound: present, the recorded device/inode, not the context itself, a git
// common directory rather than a linked worktree's git directory, and not
// bound onward.
func validateBackend(b Binding, context fileID) error {
	info, err := os.Stat(b.CommonDir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrBackendUnavailable, b.CommonDir)
	}
	device, inode := gitobj.FileID(info)
	if device != b.Device || inode != b.Inode {
		return fmt.Errorf("%w: bound device %d inode %d, found device %d inode %d", ErrBackendReplaced, b.Device, b.Inode, device, inode)
	}
	if b.id() == context {
		return fmt.Errorf("%w: binding points at the context itself", ErrBindingCycle)
	}
	if _, err := os.Lstat(filepath.Join(b.CommonDir, "commondir")); err == nil {
		return fmt.Errorf("%w: %s is a linked worktree's git directory, not a git common directory", ErrBackendUnavailable, b.CommonDir)
	}
	for _, entry := range []string{"objects", "refs", "HEAD"} {
		if _, err := os.Stat(filepath.Join(b.CommonDir, entry)); err != nil {
			return fmt.Errorf("%w: %s is not a git common directory: %v", ErrBackendUnavailable, b.CommonDir, err)
		}
	}
	target, bound, err := readBinding(filepath.Join(b.CommonDir, "config"))
	if err != nil {
		return fmt.Errorf("%w: %s carries a %s value that does not parse: %v", ErrBackendRedirects, b.CommonDir, bindingKey, err)
	}
	if bound {
		return fmt.Errorf("%w: %s is bound to %s", ErrBackendRedirects, b.CommonDir, target.CommonDir)
	}
	return nil
}

// openRecords opens the validated backend's object database; a layout gitobj
// cannot read is an unavailable backend.
func openRecords(b Binding) (*gitobj.Repo, error) {
	repo, err := gitobj.Open(b.CommonDir, b.CommonDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	return repo, nil
}

// configStamp identifies one version of a context's config file.
type configStamp struct {
	size      int64
	modNano   int64
	ctimeNano int64
	inode     uint64
}

func stampOf(info os.FileInfo) configStamp {
	ctime, inode := gitobj.StatIdentity(info)
	return configStamp{size: info.Size(), modNano: info.ModTime().UnixNano(), ctimeNano: ctime, inode: inode}
}

// storageBinding is what a store knows about its context's cc-notes.storage
// value: the config file it was read from, the validated binding when one was
// set, and the stamp that read was taken under. mu guards stamp; the rest is
// immutable after open, and Pinned views share the whole record.
type storageBinding struct {
	config  string
	binding Binding
	bound   bool

	mu    sync.Mutex
	stamp configStamp
}

// openBinding reads and validates the context's binding. The config is
// stat'ed before it is read, so a rewrite racing the read surfaces on the next
// check. Unbound contexts record only the stamp.
func openBinding(commonDir string) (*storageBinding, error) {
	config := filepath.Join(commonDir, "config")
	info, err := os.Stat(config)
	if err != nil {
		return nil, &BindingError{Config: config, Err: fmt.Errorf("%w: %v", ErrBindingMalformed, err)}
	}
	b, bound, err := readBinding(config)
	if err != nil {
		return nil, &BindingError{Config: config, Err: err}
	}
	w := &storageBinding{config: config, binding: b, bound: bound, stamp: stampOf(info)}
	if !bound {
		return w, nil
	}
	context, err := fileIDOf(commonDir)
	if err != nil {
		return nil, w.fail(fmt.Errorf("%w: cannot stat the context common directory: %v", ErrBackendUnavailable, err))
	}
	if err := validateBackend(b, context); err != nil {
		return nil, w.fail(err)
	}
	return w, nil
}

func (w *storageBinding) fail(err error) error {
	e := &BindingError{Config: w.config, Err: err}
	if w.bound {
		e.Source = w.binding.CommonDir
	}
	return e
}

// check is CheckRecords: one stat of the context config, a re-read of the
// binding only when that stat moved, and for a bound store one stat of the
// backend against the bound identity. It never spawns git.
func (w *storageBinding) check() error {
	info, err := os.Stat(w.config)
	if err != nil {
		return w.fail(fmt.Errorf("%w: %v", ErrBindingChanged, err))
	}
	stamp := stampOf(info)
	w.mu.Lock()
	moved := stamp != w.stamp
	w.mu.Unlock()
	if moved {
		if err := w.recheckBinding(); err != nil {
			return w.fail(err)
		}
		w.mu.Lock()
		w.stamp = stamp
		w.mu.Unlock()
	}
	if !w.bound {
		return nil
	}
	backend, err := os.Stat(w.binding.CommonDir)
	if err != nil {
		return w.fail(fmt.Errorf("%w: %v", ErrBackendUnavailable, err))
	}
	if device, inode := gitobj.FileID(backend); device != w.binding.Device || inode != w.binding.Inode {
		return w.fail(fmt.Errorf("%w: bound device %d inode %d, found device %d inode %d", ErrBackendReplaced, w.binding.Device, w.binding.Inode, device, inode))
	}
	return nil
}

func (w *storageBinding) recheckBinding() error {
	b, bound, err := readBinding(w.config)
	if err != nil {
		return err
	}
	switch {
	case bound && !w.bound:
		return fmt.Errorf("%w: now bound to %s; reopen the store", ErrBindingChanged, b.CommonDir)
	case !bound && w.bound:
		return fmt.Errorf("%w: binding removed; reopen the store", ErrBindingChanged)
	case bound && b != w.binding:
		return fmt.Errorf("%w: now bound to %s; reopen the store", ErrBindingChanged, b.CommonDir)
	}
	return nil
}
