package mcpserver

// anchorSetArgs is the add-tool anchor fragment (note/doc/log/runbook): the four
// repeatable anchor arrays mirroring the CLI's --commit/--path/--dir/--branch.
// Embedded anonymously so its fields flatten into the host tool's schema.
type anchorSetArgs struct {
	Commits  []string `json:"commits,omitempty" jsonschema:"commit anchors (sha or revision; resolved to full sha)"`
	Paths    []string `json:"paths,omitempty" jsonschema:"path anchors"`
	Dirs     []string `json:"dirs,omitempty" jsonschema:"directory anchors"`
	Branches []string `json:"branches,omitempty" jsonschema:"branch anchors"`
}

// anchorEditArgs is the embedded anchor-edit fragment shared by every edit tool
// (note/doc/log/runbook): the add/remove octet mirroring the CLI's
// --add-*/--rm-* anchor flags.
type anchorEditArgs struct {
	AddPaths    []string `json:"add_paths,omitempty" jsonschema:"path anchors to add"`
	RmPaths     []string `json:"rm_paths,omitempty" jsonschema:"path anchors to remove"`
	AddDirs     []string `json:"add_dirs,omitempty" jsonschema:"directory anchors to add"`
	RmDirs      []string `json:"rm_dirs,omitempty" jsonschema:"directory anchors to remove"`
	AddCommits  []string `json:"add_commits,omitempty" jsonschema:"commit anchors to add"`
	RmCommits   []string `json:"rm_commits,omitempty" jsonschema:"commit anchors to remove"`
	AddBranches []string `json:"add_branches,omitempty" jsonschema:"branch anchors to add"`
	RmBranches  []string `json:"rm_branches,omitempty" jsonschema:"branch anchors to remove"`
}

// anchorSetFlags appends the --commit/--path/--dir/--branch flags for an add
// tool, in the CLI's flag order.
func anchorSetFlags(flags []string, a anchorSetArgs) []string {
	flags = optRepeated(flags, "--commit", a.Commits)
	flags = optRepeated(flags, "--path", a.Paths)
	flags = optRepeated(flags, "--dir", a.Dirs)
	flags = optRepeated(flags, "--branch", a.Branches)
	return flags
}

// anchorEditFlags appends the --add-*/--rm-* anchor flags for an edit tool, in
// the CLI's flag order.
func anchorEditFlags(flags []string, a anchorEditArgs) []string {
	flags = optRepeated(flags, "--add-path", a.AddPaths)
	flags = optRepeated(flags, "--rm-path", a.RmPaths)
	flags = optRepeated(flags, "--add-dir", a.AddDirs)
	flags = optRepeated(flags, "--rm-dir", a.RmDirs)
	flags = optRepeated(flags, "--add-commit", a.AddCommits)
	flags = optRepeated(flags, "--rm-commit", a.RmCommits)
	flags = optRepeated(flags, "--add-branch", a.AddBranches)
	flags = optRepeated(flags, "--rm-branch", a.RmBranches)
	return flags
}
