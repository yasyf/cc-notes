package cli_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
	"github.com/yasyf/fusekit"
	"github.com/yasyf/fusekit/mountd"
)

func TestExitCodeAndLabel(t *testing.T) {
	ambiguous := &store.AmbiguousError{
		Kind:   model.KindTask,
		Prefix: "a",
		Candidates: []store.Candidate{
			{ID: model.EntityID("aaaaaaa1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Title: "one"},
			{ID: model.EntityID("aaaaaaa2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Title: "two"},
		},
	}
	cases := []struct {
		name  string
		err   error
		code  int
		label string
	}{
		{"nil", nil, 0, ""},
		{"plain error", errors.New("boom"), 1, "error"},
		{"usage", &cli.UsageError{Err: errors.New("unknown flag")}, 2, "usage"},
		{"wrapped usage", fmt.Errorf("run: %w", &cli.UsageError{Err: errors.New("bad arity")}), 2, "usage"},
		{"not found store", fmt.Errorf("resolve: %w", store.ErrNotFound), 3, "not-found"},
		{"not found ref", fmt.Errorf("load: %w", gitobj.ErrRefNotFound), 3, "not-found"},
		{"conflict", &cli.ConflictError{Msg: "already done"}, 4, "conflict"},
		{"contended", fmt.Errorf("append: %w", store.ErrContended), 4, "conflict"},
		{"sync contended", fmt.Errorf("sync: %w", ccsync.ErrSyncContended), 4, "conflict"},
		{"ambiguous", fmt.Errorf("resolve: %w", ambiguous), 5, "ambiguous"},
		// notes-layer errors the batch domain migrations return raw or lightly
		// wrapped: classify maps them without a per-domain taskErr clone.
		{"empty edit", fmt.Errorf("edit: %w", notes.ErrEmptyEdit), 2, "usage"},
		{"attachment exists", &notes.AttachmentExistsError{Name: "f.txt"}, 2, "usage"},
		{"ambiguous kinds", &notes.AmbiguousKindsError{Prefix: "ab", Matches: []notes.KindMatch{
			{Kind: model.KindNote, ID: model.EntityID("aaaaaaa1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Title: "one"},
			{Kind: model.KindTask, ID: model.EntityID("bbbbbbb2bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), Title: "two"},
		}}, 5, "ambiguous"},
		{"missing content", &notes.MissingContentError{Attachment: model.Attachment{Name: "g.txt", OID: "abc123", Size: 1}}, 1, "error"},
		{"remote missing", fmt.Errorf("sync: %w", ccsync.ErrRemoteNotFound), 1, "error"},
		// Mount-holder conflicts: exit 4.
		{"holder busy", fmt.Errorf("mount: %w", mountd.ErrBusy), 4, "conflict"},
		{"foreign mount", fmt.Errorf("mount: %w", mountd.ErrForeignMount), 4, "conflict"},
		{"base mismatch", fmt.Errorf("mount: %w", mountd.ErrBaseMismatch), 4, "conflict"},
		// Every other holder-class error and the fuse sentinels: exit 1.
		{"holder unavailable", fmt.Errorf("mount: %w", mountd.ErrHolderUnavailable), 1, "error"},
		{"tcc denied", fmt.Errorf("mount: %w", mountd.ErrTCCDenied), 1, "error"},
		{"holder unmount wedged", fmt.Errorf("unmount: %w", mountd.ErrUnmountWedged), 1, "error"},
		{"mount timeout", fmt.Errorf("mount: %w", mountd.ErrMountTimeout), 1, "error"},
		{"mount failed", fmt.Errorf("mount: %w", mountd.ErrMountFailed), 1, "error"},
		{"unknown class", fmt.Errorf("mount: %w", mountd.ErrUnknownClass), 1, "error"},
		{"cannot host", fmt.Errorf("spawn: %w", mountd.ErrCannotHost), 1, "error"},
		{"fuse unavailable", fmt.Errorf("mount: %w", fusekit.ErrFuseUnavailable), 1, "error"},
		// RemoteHost dual-wraps a TCC denial with fusekit.ErrMountNotLive; it must
		// still classify as a plain error, never a conflict.
		{"overlay tcc", fmt.Errorf("mount: %w: %w", fusekit.ErrMountNotLive, mountd.ErrTCCDenied), 1, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cli.ExitCode(tc.err); got != tc.code {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.code)
			}
			if got := cli.Label(tc.err); got != tc.label {
				t.Errorf("Label(%v) = %q, want %q", tc.err, got, tc.label)
			}
		})
	}
}

// TestMessageTrimsNotesPrefix: a raw notes error renders under a classify label
// without the redundant "cc-notes: " program prefix, so it reads identically to
// one funnelled through taskErr. A prefix-free error is untouched.
func TestMessageTrimsNotesPrefix(t *testing.T) {
	id := model.EntityID("aaaaaaa1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"plain error untouched", errors.New("boom"), "boom"},
		{"raw notes conflict", &notes.ConflictError{ID: id, Msg: "is already done"}, "aaaaaaa is already done"},
		{"attachment exists", &notes.AttachmentExistsError{Name: "f.txt"}, `attachment "f.txt" already exists`},
		{"missing content", &notes.MissingContentError{Attachment: model.Attachment{Name: "g.txt", OID: "abc123", Size: 1}}, `attachment "g.txt" (oid abc123) not present locally`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cli.Message(tc.err); got != tc.want {
				t.Errorf("Message(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestHint: a *notes.MissingContentError carries the sync remediation and a
// *notes.AttachmentExistsError the --replace remediation (both matched through a
// wrap); every other error carries none.
func TestHint(t *testing.T) {
	missing := &notes.MissingContentError{Attachment: model.Attachment{Name: "g.txt", OID: "abc123", Size: 1}}
	if got, want := cli.Hint(fmt.Errorf("open: %w", missing)), "run `cc-notes sync` to download it"; got != want {
		t.Errorf("Hint(missing content) = %q, want %q", got, want)
	}
	exists := &notes.AttachmentExistsError{Name: "f.txt"}
	if got, want := cli.Hint(fmt.Errorf("attach: %w", exists)), "pass --replace to overwrite it"; got != want {
		t.Errorf("Hint(attachment exists) = %q, want %q", got, want)
	}
	for _, err := range []error{
		errors.New("boom"),
		&notes.ConflictError{ID: model.EntityID("aaaaaaa1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Msg: "done"},
	} {
		if got := cli.Hint(err); got != "" {
			t.Errorf("Hint(%v) = %q, want empty", err, got)
		}
	}
}
