package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type noteAddArgs struct {
	Title  string   `json:"title" jsonschema:"short handle for the note"`
	Body   string   `json:"body,omitempty" jsonschema:"note body (markdown)"`
	Labels []string `json:"labels,omitempty" jsonschema:"labels (echoed as 'tags' in the note DTO)"`
	anchorSetArgs
	Attach []string `json:"attach,omitempty" jsonschema:"file paths to attach via git-lfs (uploaded on sync)"`
}

type noteEditArgs struct {
	ID        string   `json:"id" jsonschema:"note id prefix"`
	Title     string   `json:"title,omitempty" jsonschema:"new title"`
	Body      string   `json:"body,omitempty" jsonschema:"new body"`
	AddLabels []string `json:"add_labels,omitempty" jsonschema:"labels to add"`
	RmLabels  []string `json:"rm_labels,omitempty" jsonschema:"labels to remove"`
	anchorEditArgs
	Attach        []string `json:"attach,omitempty" jsonschema:"file paths to attach via git-lfs"`
	Replace       bool     `json:"replace,omitempty" jsonschema:"allow attach to overwrite a live attachment with the same name"`
	RmAttachments []string `json:"rm_attachments,omitempty" jsonschema:"attachment names to remove"`
}

func registerNote(ts *toolset, b *bridge) {
	addTool(ts, &mcp.Tool{Name: "note_add", Description: "Record a durable fact or decision as a note (git-synced, optionally anchored to commits/paths/dirs/branches)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in noteAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = anchorSetFlags(flags, in.anchorSetArgs)
			flags = optRepeated(flags, "--attach", in.Attach)
			return b.run(ctx, argvFor([]string{"note", "add"}, flags, in.Title)...)
		})

	addTool(ts, &mcp.Tool{Name: "note_edit", Description: "Edit a note: title, body, labels, anchors, and attachments."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in noteEditArgs) (*mcp.CallToolResult, any, error) {
			flags, err := noteDocEditFlags(in)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{"note", "edit"}, flags, in.ID)...)
		})

	registerNoteDocShared(ts, b, "note")
}

// noteDocEditFlags builds the shared edit flags (including --json) for a note;
// doc edit reuses this then appends --when. The id positional is added by the
// caller via argvFor.
func noteDocEditFlags(in noteEditArgs) ([]string, error) {
	flags := []string{"--json"}
	flags = optStr(flags, "--title", in.Title)
	flags, err := freeTextFlag(flags, "--body", in.Body)
	if err != nil {
		return nil, err
	}
	flags = optRepeated(flags, "--add-label", in.AddLabels)
	flags = optRepeated(flags, "--rm-label", in.RmLabels)
	flags = anchorEditFlags(flags, in.anchorEditArgs)
	flags = optRepeated(flags, "--attach", in.Attach)
	flags = optBool(flags, "--replace", in.Replace)
	flags = optRepeated(flags, "--rm-attachment", in.RmAttachments)
	return flags, nil
}

type entityIDArgs struct {
	ID string `json:"id" jsonschema:"entity id prefix"`
}

type commentArgs struct {
	ID   string `json:"id" jsonschema:"entity id prefix"`
	Body string `json:"body" jsonschema:"comment text"`
}

type entityListArgs struct {
	Labels            []string `json:"labels,omitempty" jsonschema:"require every label (ANDed; echoed as 'tags' in the DTO)"`
	Path              string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit            string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir               string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch            string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
	All               bool     `json:"all,omitempty" jsonschema:"include tombstoned entities"`
	IncludeSuperseded bool     `json:"include_superseded,omitempty" jsonschema:"include superseded entities"`
}

type entitySearchArgs struct {
	Query  string   `json:"query" jsonschema:"search query (matches title, labels, body)"`
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed; echoed as 'tags' in the DTO)"`
	Limit  *int     `json:"limit,omitempty" jsonschema:"maximum results (0 = all; default 20)"`
	Author string   `json:"author,omitempty" jsonschema:"require author"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
}

type supersedeArgs struct {
	ID    string `json:"id" jsonschema:"the OLD entity being replaced"`
	By    string `json:"by" jsonschema:"the NEW entity that replaces it"`
	Clear bool   `json:"clear,omitempty" jsonschema:"clear the supersede edge instead of adding it"`
}

type expireArgs struct {
	ID     string `json:"id" jsonschema:"entity id prefix"`
	Reason string `json:"reason,omitempty" jsonschema:"why it is out of date"`
	Clear  bool   `json:"clear,omitempty" jsonschema:"remove the out-of-date flag instead of setting it"`
}

type reviewArgs struct {
	StaleAfter string `json:"stale_after,omitempty" jsonschema:"staleness threshold (Go duration)"`
	Drift      bool   `json:"drift,omitempty" jsonschema:"limit to drifted entities"`
	Unverified bool   `json:"unverified,omitempty" jsonschema:"limit to never-verified entities"`
	Expired    bool   `json:"expired,omitempty" jsonschema:"limit to expired entities"`
}

// registerNoteDocShared registers the rm/list/show/search/verify/supersede/
// expire/review tools common to note and doc under the given noun.
func registerNoteDocShared(ts *toolset, b *bridge, noun string) {
	idTool(ts, b, noun+"_rm", "Tombstone a "+noun+".", noun, "rm")

	addTool(ts, &mcp.Tool{Name: noun + "_list", Description: "List " + noun + "s, optionally filtered by label and anchors."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entityListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optBool(flags, "--all", in.All)
			flags = optBool(flags, "--include-superseded", in.IncludeSuperseded)
			return b.run(ctx, argvFor([]string{noun, "list"}, flags)...)
		})

	idTool(ts, b, noun+"_show", "Show one "+noun+" with its verdict and attachments.", noun, "show")

	addTool(ts, &mcp.Tool{Name: noun + "_search", Description: "Ranked search across " + noun + " titles, labels, and bodies."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entitySearchArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optInt(flags, "--limit", in.Limit)
			flags = optStr(flags, "--author", in.Author)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optStr(flags, "--commit", in.Commit)
			return b.run(ctx, argvFor([]string{noun, "search"}, flags, in.Query)...)
		})

	idTool(ts, b, noun+"_verify", "Re-verify a "+noun+", refreshing its witness against current HEAD.", noun, "verify")

	addTool(ts, &mcp.Tool{Name: noun + "_supersede", Description: "Record that a NEW " + noun + " replaces an OLD one (or remove the edge)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in supersedeArgs) (*mcp.CallToolResult, any, error) {
			flags := optStr([]string{"--json"}, "--by", in.By)
			flags = optBool(flags, "--clear", in.Clear)
			return b.run(ctx, argvFor([]string{noun, "supersede"}, flags, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: noun + "_expire", Description: "Flag a " + noun + " as out of date (or clear the flag)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in expireArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--reason", in.Reason)
			flags = optBool(flags, "--clear", in.Clear)
			return b.run(ctx, argvFor([]string{noun, "expire"}, flags, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: noun + "_review", Description: "Surface " + noun + "s needing attention (drifted, never-verified, or expired), each with a verdict."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in reviewArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--stale-after", in.StaleAfter)
			flags = optBool(flags, "--drift", in.Drift)
			flags = optBool(flags, "--unverified", in.Unverified)
			flags = optBool(flags, "--expired", in.Expired)
			return b.run(ctx, argvFor([]string{noun, "review"}, flags)...)
		})
}
