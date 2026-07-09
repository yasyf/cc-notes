package cli

import (
	"errors"

	"github.com/spf13/pflag"
)

// This file holds the canonical flag-name literals of the harmonized CLI
// vocabulary; commands bind through these helpers so the noun groups cannot
// drift apart, and the mcpserver argv literals are cross-checked against them.
//
// Duration flags are bound at their own commands, not here: they split by
// meaning, not spelling. An age-threshold is a string parsed by parseDuration
// (empty defers to env, then git config, then default); an instant-cutoff is a
// string parsed by parseWhen (Go duration or RFC3339); a flag with a fixed
// default stays a plain DurationVar.

func bindJSON(f *pflag.FlagSet, p *bool) {
	f.BoolVar(p, "json", false, "emit JSON")
}

func bindBody(f *pflag.FlagSet, p *string, usage string) {
	f.StringVar(p, "body", "", usage)
}

func bindLabels(f *pflag.FlagSet, p *[]string, usage string) {
	f.StringArrayVar(p, "label", nil, usage)
}

type labelEdits struct {
	add []string
	rm  []string
}

func (e *labelEdits) bind(f *pflag.FlagSet) {
	f.StringArrayVar(&e.add, "add-label", nil, "add label (repeatable)")
	f.StringArrayVar(&e.rm, "rm-label", nil, "remove label (repeatable)")
}

type anchorSets struct {
	commits  []string
	paths    []string
	dirs     []string
	branches []string
}

func (a *anchorSets) bind(f *pflag.FlagSet) {
	f.StringArrayVar(&a.commits, "commit", nil, "commit anchor (repeatable)")
	f.StringArrayVar(&a.paths, "path", nil, "path anchor (repeatable)")
	f.StringArrayVar(&a.dirs, "dir", nil, "directory anchor (repeatable)")
	f.StringArrayVar(&a.branches, "branch", nil, "branch anchor (repeatable)")
}

type anchorEdits struct {
	addCommits  []string
	rmCommits   []string
	addPaths    []string
	rmPaths     []string
	addDirs     []string
	rmDirs      []string
	addBranches []string
	rmBranches  []string
}

func (a *anchorEdits) bind(f *pflag.FlagSet) {
	f.StringArrayVar(&a.addCommits, "add-commit", nil, "add commit anchor (repeatable)")
	f.StringArrayVar(&a.rmCommits, "rm-commit", nil, "remove commit anchor (repeatable)")
	f.StringArrayVar(&a.addPaths, "add-path", nil, "add path anchor (repeatable)")
	f.StringArrayVar(&a.rmPaths, "rm-path", nil, "remove path anchor (repeatable)")
	f.StringArrayVar(&a.addDirs, "add-dir", nil, "add directory anchor (repeatable)")
	f.StringArrayVar(&a.rmDirs, "rm-dir", nil, "remove directory anchor (repeatable)")
	f.StringArrayVar(&a.addBranches, "add-branch", nil, "add branch anchor (repeatable)")
	f.StringArrayVar(&a.rmBranches, "rm-branch", nil, "remove branch anchor (repeatable)")
}

type anchorFilters struct {
	commit string
	path   string
	dir    string
	branch string
}

func (a *anchorFilters) bind(f *pflag.FlagSet) {
	f.StringVar(&a.commit, "commit", "", "require commit anchor")
	f.StringVar(&a.path, "path", "", "require path anchor")
	f.StringVar(&a.dir, "dir", "", "require directory anchor")
	f.StringVar(&a.branch, "branch", "", "require branch anchor")
}

type branchTarget struct {
	branch  string
	backlog bool
}

func (b *branchTarget) bind(f *pflag.FlagSet) {
	f.StringVar(&b.branch, "branch", "", "task branch (default: current branch)")
	f.BoolVar(&b.backlog, "backlog", false, "put on the shared backlog (no branch)")
}

// validate rejects --branch together with --backlog as a UsageError. It keys off
// branchChanged (cmd.Flags().Changed("branch")), not b.branch != "", so it
// matches the op-emission guard: an explicit --branch "" still conflicts with
// --backlog rather than slipping through to an invalid-empty-branch error later.
func (b *branchTarget) validate(branchChanged bool) error {
	if branchChanged && b.backlog {
		return &UsageError{Err: errors.New("--branch and --backlog are mutually exclusive")}
	}
	return nil
}
