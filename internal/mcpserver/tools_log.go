package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type logAddArgs struct {
	Title    string   `json:"title" jsonschema:"short handle for the log"`
	Entry    string   `json:"entry,omitempty" jsonschema:"optional first entry text"`
	Tags     []string `json:"tags,omitempty" jsonschema:"tags"`
	Commits  []string `json:"commits,omitempty" jsonschema:"commit anchors"`
	Paths    []string `json:"paths,omitempty" jsonschema:"path anchors"`
	Dirs     []string `json:"dirs,omitempty" jsonschema:"directory anchors"`
	Branches []string `json:"branches,omitempty" jsonschema:"branch anchors"`
	Attach   []string `json:"attach,omitempty" jsonschema:"file paths to attach via git-lfs"`
}

type logAppendArgs struct {
	ID      string   `json:"id" jsonschema:"log id prefix"`
	Text    string   `json:"text,omitempty" jsonschema:"entry text (required unless attach is given)"`
	Attach  []string `json:"attach,omitempty" jsonschema:"file paths to attach via git-lfs"`
	Replace bool     `json:"replace,omitempty" jsonschema:"allow attach to overwrite a live attachment with the same name"`
}

type logEditArgs struct {
	ID            string   `json:"id" jsonschema:"log id prefix"`
	Title         string   `json:"title,omitempty" jsonschema:"new title"`
	AddTags       []string `json:"add_tags,omitempty" jsonschema:"tags to add"`
	RmTags        []string `json:"rm_tags,omitempty" jsonschema:"tags to remove"`
	AddPaths      []string `json:"add_paths,omitempty" jsonschema:"path anchors to add"`
	RmPaths       []string `json:"rm_paths,omitempty" jsonschema:"path anchors to remove"`
	AddDirs       []string `json:"add_dirs,omitempty" jsonschema:"directory anchors to add"`
	RmDirs        []string `json:"rm_dirs,omitempty" jsonschema:"directory anchors to remove"`
	AddCommits    []string `json:"add_commits,omitempty" jsonschema:"commit anchors to add"`
	RmCommits     []string `json:"rm_commits,omitempty" jsonschema:"commit anchors to remove"`
	AddBranches   []string `json:"add_branches,omitempty" jsonschema:"branch anchors to add"`
	RmBranches    []string `json:"rm_branches,omitempty" jsonschema:"branch anchors to remove"`
	RmAttachments []string `json:"rm_attachments,omitempty" jsonschema:"attachment names to remove"`
}

type logListArgs struct {
	Tags   []string `json:"tags,omitempty" jsonschema:"require every tag (ANDed)"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
	All    bool     `json:"all,omitempty" jsonschema:"include tombstoned logs"`
}

func registerLog(srv *mcp.Server, b *bridge) {
	mcp.AddTool(srv, &mcp.Tool{Name: "log_add", Description: "Create an append-only log (incident timeline, rollout log, debugging record)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logAddArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--entry", in.Entry)
			flags = optRepeated(flags, "--tag", in.Tags)
			flags = optRepeated(flags, "--commit", in.Commits)
			flags = optRepeated(flags, "--path", in.Paths)
			flags = optRepeated(flags, "--dir", in.Dirs)
			flags = optRepeated(flags, "--branch", in.Branches)
			flags = optRepeated(flags, "--attach", in.Attach)
			return b.run(ctx, argvFor([]string{"log", "add"}, flags, in.Title)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "log_append", Description: "Append one entry to a log, and/or attach files. Entries are append-only."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logAppendArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--message", in.Text)
			flags = optRepeated(flags, "--attach", in.Attach)
			flags = optBool(flags, "--replace", in.Replace)
			return b.run(ctx, argvFor([]string{"log", "append"}, flags, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "log_edit", Description: "Edit a log's title, tags, and anchors (entries are append-only)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logEditArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--title", in.Title)
			flags = optRepeated(flags, "--add-tag", in.AddTags)
			flags = optRepeated(flags, "--rm-tag", in.RmTags)
			flags = optRepeated(flags, "--add-path", in.AddPaths)
			flags = optRepeated(flags, "--rm-path", in.RmPaths)
			flags = optRepeated(flags, "--add-dir", in.AddDirs)
			flags = optRepeated(flags, "--rm-dir", in.RmDirs)
			flags = optRepeated(flags, "--add-commit", in.AddCommits)
			flags = optRepeated(flags, "--rm-commit", in.RmCommits)
			flags = optRepeated(flags, "--add-branch", in.AddBranches)
			flags = optRepeated(flags, "--rm-branch", in.RmBranches)
			flags = optRepeated(flags, "--rm-attachment", in.RmAttachments)
			return b.run(ctx, argvFor([]string{"log", "edit"}, flags, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "log_rm", Description: "Tombstone a log."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entityIDArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"log", "rm"}, []string{"--json"}, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "log_list", Description: "List logs, optionally filtered by tag and anchors."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--tag", in.Tags)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optBool(flags, "--all", in.All)
			return b.run(ctx, argvFor([]string{"log", "list"}, flags)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "log_show", Description: "Show one log with its entries in chronological order."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entityIDArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"log", "show"}, []string{"--json"}, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "log_search", Description: "Ranked search across log titles, tags, and entry text."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entitySearchArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--tag", in.Tags)
			flags = optInt(flags, "--limit", in.Limit)
			flags = optStr(flags, "--author", in.Author)
			flags = optStr(flags, "--anchor-path", in.AnchorPath)
			flags = optStr(flags, "--anchor-dir", in.AnchorDir)
			flags = optStr(flags, "--anchor-branch", in.AnchorBranch)
			flags = optStr(flags, "--anchor-commit", in.AnchorCommit)
			return b.run(ctx, argvFor([]string{"log", "search"}, flags, in.Query)...)
		})
}
