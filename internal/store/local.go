package store

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const (
	// LocalLabel keeps an entity on this clone: sync never pushes it, and the
	// negative push refspec cc-notes maintains keeps a plain git push from
	// publishing it either.
	LocalLabel = "local"
	// SyncedLabel publishes an entity a default would otherwise keep local.
	// LocalLabel wins when both are present.
	SyncedLabel = "synced"
	// LocalPushInclude is the config file, relative to the records common git
	// directory, recording every local entity ref as cc-notes.secluded and as
	// a negative push refspec for every remote wired with refs.PushRefspec.
	// The records config includes it, so a plain git push honors it; cc-notes
	// rewrites it whole, never through git config.
	LocalPushInclude = "cc-notes/local-push.config"
)

// LocalPolicy decides which entities stay on this clone. An entity is local
// when it carries LocalLabel, or when it does not carry SyncedLabel and it
// carries one of Labels or an attachment larger than MaxBytes or whose name
// matches one of Globs.
type LocalPolicy struct {
	Labels   []string
	MaxBytes int64
	Globs    []string
}

// DefaultLocalPolicy keeps lane briefs, raw captures, and bulky or raw-log
// attachments off the remote until an entity opts in with SyncedLabel.
// cc-notes.localLabel, cc-notes.localAttachBytes, and cc-notes.localAttachGlob
// in git config replace the matching field.
var DefaultLocalPolicy = LocalPolicy{
	Labels:   []string{LocalLabel, "lane-brief", "raw-capture"},
	MaxBytes: 10 << 20,
	Globs:    []string{"*.log", "*.out", "*.jsonl"},
}

// Reason returns why meta's entity stays local, or "" when it syncs.
func (p LocalPolicy) Reason(meta model.Meta) string {
	if slices.Contains(meta.Labels, LocalLabel) {
		return "label " + LocalLabel
	}
	if slices.Contains(meta.Labels, SyncedLabel) {
		return ""
	}
	for _, label := range p.Labels {
		if slices.Contains(meta.Labels, label) {
			return "label " + label
		}
	}
	for _, att := range meta.Attachments {
		if reason := p.attachmentReason(att); reason != "" {
			return reason
		}
	}
	return ""
}

func (p LocalPolicy) attachmentReason(att model.Attachment) string {
	if p.MaxBytes > 0 && att.Size > p.MaxBytes {
		return fmt.Sprintf("attachment %s is %d bytes, over %d", att.Name, att.Size, p.MaxBytes)
	}
	for _, glob := range p.Globs {
		if ok, _ := path.Match(glob, att.Name); ok {
			return fmt.Sprintf("attachment %s matches %s", att.Name, glob)
		}
	}
	return ""
}

// LocalAttachmentError refuses attaching a file a local default covers to an
// entity a remote already has: the default cannot withhold a published entity,
// so the file would publish with it.
type LocalAttachmentError struct {
	ID     model.EntityID
	Reason string
}

func (e *LocalAttachmentError) Error() string {
	return fmt.Sprintf("%s is published and %s, which keeps a file local: attach it to a local entity (add one with --local), "+
		"keep this one local with `ccn local mark %s`, or publish the file with it by labelling %s %s", e.ID, e.Reason, e.ID, e.ID, SyncedLabel)
}

// LocalPolicy reads the policy from the records config once per Store, falling
// back to DefaultLocalPolicy field by field.
func (s *Store) LocalPolicy(ctx context.Context) (LocalPolicy, error) {
	s.policy.once.Do(func() {
		s.policy.value, s.policy.err = readLocalPolicy(ctx, s)
	})
	return s.policy.value, s.policy.err
}

func readLocalPolicy(ctx context.Context, s *Store) (LocalPolicy, error) {
	pairs, err := s.RecordsGit.ConfigGetRegexp(ctx, `^cc-notes\.local`)
	if err != nil {
		return LocalPolicy{}, fmt.Errorf("local policy: %w", err)
	}
	policy := DefaultLocalPolicy
	var labels, globs []string
	for _, pair := range pairs {
		switch strings.ToLower(pair[0]) {
		case "cc-notes.locallabel":
			labels = append(labels, pair[1])
		case "cc-notes.localattachglob":
			globs = append(globs, pair[1])
		case "cc-notes.localattachbytes":
			n, err := strconv.ParseInt(pair[1], 10, 64)
			if err != nil {
				return LocalPolicy{}, fmt.Errorf("local policy: cc-notes.localAttachBytes %q: %w", pair[1], err)
			}
			policy.MaxBytes = n
		}
	}
	if labels != nil {
		policy.Labels = append([]string{LocalLabel}, labels...)
	}
	if globs != nil {
		policy.Globs = globs
	}
	return policy, nil
}

// LocalReason folds ref and returns why it stays local, or "" when it syncs.
func (s *Store) LocalReason(ctx context.Context, ref string) (string, error) {
	snap, err := s.Load(ctx, ref)
	if err != nil {
		return "", err
	}
	policy, err := s.LocalPolicy(ctx)
	if err != nil {
		return "", err
	}
	return policy.Reason(snap.Meta()), nil
}

// Secluded returns the refs the local push include currently excludes.
func (s *Store) Secluded() (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(s.recordsCommonDir, LocalPushInclude))
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", LocalPushInclude, err)
	}
	out := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "secluded = "); ok {
			out[value] = true
		}
	}
	return out, nil
}

// Seclude adds refs to and removes release from the local push include,
// rewriting it whole under an exclusive lock, and makes sure the records
// config includes it.
func (s *Store) Seclude(ctx context.Context, add, release []string) (err error) {
	dir := filepath.Join(s.recordsCommonDir, filepath.Dir(LocalPushInclude))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("seclude: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(s.recordsCommonDir, LocalPushInclude+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("seclude: %w", err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("seclude: %w", closeErr)
		}
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("seclude: %w", err)
	}
	current, err := s.Secluded()
	if err != nil {
		return err
	}
	for _, ref := range add {
		current[ref] = true
	}
	for _, ref := range release {
		delete(current, ref)
	}
	remotes, err := s.wiredRemotes(ctx)
	if err != nil {
		return fmt.Errorf("seclude: %w", err)
	}
	if err := writeLocalPush(filepath.Join(s.recordsCommonDir, LocalPushInclude), remotes, current); err != nil {
		return fmt.Errorf("seclude: %w", err)
	}
	return s.ensureLocalInclude(ctx)
}

// wiredRemotes lists the remotes whose push config carries refs.PushRefspec.
func (s *Store) wiredRemotes(ctx context.Context) ([]string, error) {
	pairs, err := s.RecordsGit.ConfigGetRegexp(ctx, `^remote\..*\.push$`)
	if err != nil {
		return nil, err
	}
	var remotes []string
	for _, pair := range pairs {
		if pair[1] != refs.PushRefspec {
			continue
		}
		remote := strings.TrimSuffix(strings.TrimPrefix(pair[0], "remote."), ".push")
		if !slices.Contains(remotes, remote) {
			remotes = append(remotes, remote)
		}
	}
	slices.Sort(remotes)
	return remotes, nil
}

func writeLocalPush(file string, remotes []string, secluded map[string]bool) error {
	ordered := make([]string, 0, len(secluded))
	for ref := range secluded {
		ordered = append(ordered, ref)
	}
	slices.Sort(ordered)
	var b strings.Builder
	b.WriteString("[cc-notes]\n")
	for _, ref := range ordered {
		fmt.Fprintf(&b, "\tsecluded = %s\n", ref)
	}
	for _, remote := range remotes {
		fmt.Fprintf(&b, "[remote %q]\n", remote)
		for _, ref := range ordered {
			fmt.Fprintf(&b, "\tpush = ^%s\n", ref)
		}
	}
	staged := file + ".tmp"
	if err := os.WriteFile(staged, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(staged, file)
}

func (s *Store) ensureLocalInclude(ctx context.Context) error {
	includes, err := s.RecordsGit.ConfigGetAll(ctx, "include.path")
	if err != nil {
		return fmt.Errorf("seclude: %w", err)
	}
	if slices.Contains(includes, LocalPushInclude) {
		return nil
	}
	if err := s.RecordsGit.ConfigAdd(ctx, "include.path", LocalPushInclude); err != nil {
		return fmt.Errorf("seclude: %w", err)
	}
	return nil
}

// Defaulted reports whether reason comes from a default rather than the
// explicit LocalLabel. A default never withholds an entity a remote already
// has: withholding it would freeze the remote copy at a stale tip.
func Defaulted(reason string) bool {
	return reason != "" && reason != "label "+LocalLabel
}

// Published reports whether any remote's tracking namespace holds ref.
func (s *Store) Published(ctx context.Context, ref string) (bool, error) {
	parsed, err := refs.Parse(ref)
	if err != nil {
		return false, err
	}
	tracked, err := s.RecordsGit.Refs(ctx, "refs/cc-notes-sync/*/"+strings.TrimPrefix(refs.For(parsed.Kind, parsed.ID), refs.Namespace))
	if err != nil {
		return false, err
	}
	return len(tracked) > 0, nil
}

// track keeps ref's entry in the local push include in step with snap after a
// write, so a plain git push between writes never publishes a local entity.
// It refuses the write when a default would keep one of the added attachments
// local but ref is already published.
func (s *Store) track(ctx context.Context, ref string, snap model.Snapshot, added []model.Attachment) error {
	policy, err := s.LocalPolicy(ctx)
	if err != nil {
		return err
	}
	reason := policy.Reason(snap.Meta())
	if Defaulted(reason) {
		published, err := s.Published(ctx, ref)
		if err != nil {
			return err
		}
		if published {
			if err := policy.refuse(ref, added); err != nil {
				return err
			}
			reason = ""
		}
	}
	local := reason != ""
	secluded, err := s.Secluded()
	if err != nil {
		return err
	}
	switch {
	case local && !secluded[ref]:
		return s.Seclude(ctx, []string{ref}, nil)
	case !local && secluded[ref]:
		return s.Seclude(ctx, nil, []string{ref})
	}
	return nil
}

func (p LocalPolicy) refuse(ref string, added []model.Attachment) error {
	for _, att := range added {
		if reason := p.attachmentReason(att); reason != "" {
			parsed, err := refs.Parse(ref)
			if err != nil {
				return err
			}
			return &LocalAttachmentError{ID: parsed.ID, Reason: reason}
		}
	}
	return nil
}
