package cli

import (
	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// appendVerb is the shared skeleton behind the entity mutation verbs: open the
// store, auto-install refspecs, build the ops before the load so a bad flag or
// body mutates nothing, load and guard the snapshot, append, and echo the
// result through the kind's printer. A nil guard skips the transition check.
func (k kindSpec[T]) appendVerb(cmd *cobra.Command, prefix string, jsonOut bool, guard func(T) error, buildOps func() ([]model.Op, error)) error {
	ctx := cmd.Context()
	s, err := openStore()
	if err != nil {
		return err
	}
	if err := autoInstall(ctx, cmd, s.Git); err != nil {
		return err
	}
	ops, err := buildOps()
	if err != nil {
		return err
	}
	ref, snap, err := k.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	if guard != nil {
		if err := guard(snap); err != nil {
			return err
		}
	}
	next, err := s.Append(ctx, ref, ops)
	if err != nil {
		return err
	}
	return k.print(cmd, s, next.(T), jsonOut)
}

// statusVerb builds a "<use> ID" command that transitions an entity to status,
// gated by the kind's transition guard.
func (k kindSpec[T]) statusVerb(use, status string, guard func(T) error, op model.Op) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: "Mark a " + k.noun + " " + status,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return k.appendVerb(cmd, args[0], jsonOut, guard, func() ([]model.Op, error) {
				return []model.Op{op}, nil
			})
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// commentVerb builds the "comment ID BODY" command, gated when guard is
// non-nil (runbooks refuse comments once archived; siblings never gate).
func (k kindSpec[T]) commentVerb(guard func(T) error) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "comment ID BODY",
		Short: "Append a comment; BODY - reads stdin",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return k.appendVerb(cmd, args[0], jsonOut, guard, func() ([]model.Op, error) {
				body, err := bodyArg(cmd, args[1])
				if err != nil {
					return nil, err
				}
				return []model.Op{model.AddComment{Body: body}}, nil
			})
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// rmVerb builds the "rm ID" command that tombstones an attachable entity. Note,
// doc, and log share the DeleteNote storage op.
func (k kindSpec[T]) rmVerb() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm ID",
		Short: "Tombstone a " + k.noun,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return k.appendVerb(cmd, args[0], jsonOut, nil, func() ([]model.Op, error) {
				return []model.Op{model.DeleteNote{}}, nil
			})
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// showVerb builds the read-only "show ID" command; short is the kind's own
// help line and show gathers the bespoke read-side data.
func (k kindSpec[T]) showVerb(short string, show func(cmd *cobra.Command, s *store.Store, prefix string, jsonOut bool) error) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: short,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			return show(cmd, s, args[0], jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}
