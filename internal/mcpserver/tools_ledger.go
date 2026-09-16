package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ledgerAddArgs struct {
	Title   string   `json:"title" jsonschema:"short handle for the ledger"`
	Body    string   `json:"body,omitempty" jsonschema:"ledger description (echoed as 'description' in the ledger DTO)"`
	Columns []string `json:"columns,omitempty" jsonschema:"field names in display order"`
	Labels  []string `json:"labels,omitempty" jsonschema:"labels"`
	anchorSetArgs
}

type ledgerListArgs struct {
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed; echoed as 'tags' in the DTO)"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
	All    bool     `json:"all,omitempty" jsonschema:"include archived ledgers (default active only)"`
}

type ledgerEditArgs struct {
	ID        string   `json:"id" jsonschema:"ledger id prefix"`
	Title     string   `json:"title,omitempty" jsonschema:"new title"`
	Body      string   `json:"body,omitempty" jsonschema:"new description"`
	Columns   []string `json:"columns,omitempty" jsonschema:"replacement field names in display order"`
	AddLabels []string `json:"add_labels,omitempty" jsonschema:"labels to add"`
	RmLabels  []string `json:"rm_labels,omitempty" jsonschema:"labels to remove"`
	anchorEditArgs
}

type ledgerSearchArgs struct {
	Query  string   `json:"query" jsonschema:"search query (matches title, labels, description, row keys, and field values)"`
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed; echoed as 'tags' in the DTO)"`
	Limit  *int     `json:"limit,omitempty" jsonschema:"maximum results (0 = all; default 20)"`
	Author string   `json:"author,omitempty" jsonschema:"require author"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
}

// ledgerRowArg is one row of a sync: the key that identifies it within the
// ledger and the fields to write.
type ledgerRowArg struct {
	Key    string            `json:"key" jsonschema:"row key, unique within the ledger"`
	Fields map[string]string `json:"fields" jsonschema:"field name to value"`
}

type ledgerSyncArgs struct {
	ID    string         `json:"id" jsonschema:"ledger id prefix"`
	Rows  []ledgerRowArg `json:"rows" jsonschema:"the whole row set; each row's fields merge into the row already keyed by it"`
	Prune bool           `json:"prune,omitempty" jsonschema:"remove every row the set omits"`
}

type ledgerRowSetArgs struct {
	ID      string            `json:"id" jsonschema:"ledger id prefix"`
	Key     string            `json:"key" jsonschema:"row key"`
	Fields  map[string]string `json:"fields" jsonschema:"field name to value"`
	Replace bool              `json:"replace,omitempty" jsonschema:"drop the fields this call omits instead of leaving them standing"`
}

type ledgerRowRefArgs struct {
	ID  string `json:"id" jsonschema:"ledger id prefix"`
	Key string `json:"key" jsonschema:"row key"`
}

type ledgerRowListArgs struct {
	ID    string            `json:"id" jsonschema:"ledger id prefix"`
	Where map[string]string `json:"where,omitempty" jsonschema:"keep only rows whose fields hold every pair"`
}

func registerLedger(ts *toolset, b *bridge) {
	addTool(ts, &mcp.Tool{Name: "ledger_add", Description: "Create a ledger: a keyed row set an agent refreshes in place from an external system, one row per tracked unit (an open pull request, a migrating package, a stack awaiting apply). The ack is a summary carrying the row tally; ledger_show reads the rows back."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ledgerAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--column", in.Columns)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = anchorSetFlags(flags, in.anchorSetArgs)
			return b.run(ctx, argvFor([]string{"ledger", "add"}, flags, in.Title)...)
		})

	addTool(ts, &mcp.Tool{Name: "ledger_list", Description: "List ledgers, optionally filtered by label and anchors (active only unless all is set). Returns summaries carrying the row tally; ledger_show reads the rows back."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ledgerListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optBool(flags, "--all", in.All)
			return b.run(ctx, argvFor([]string{"ledger", "list"}, flags)...)
		})

	addTool(ts, &mcp.Tool{Name: "ledger_show", Description: "Show one ledger with its rows, narrowed to the rows whose fields hold every where pair."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ledgerRowListArgs) (*mcp.CallToolResult, any, error) {
			flags := append([]string{"--json"}, fieldFlags("--where", in.Where)...)
			return b.run(ctx, argvFor([]string{"ledger", "show"}, flags, in.ID)...)
		})

	statusTools(ts, b, "ledger", "activate", "archive")

	addTool(ts, &mcp.Tool{Name: "ledger_edit", Description: "Edit a ledger's title, description, column order, labels, and anchors."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ledgerEditArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--title", in.Title)
			flags, err := freeTextFlag(flags, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--column", in.Columns)
			flags = optRepeated(flags, "--add-label", in.AddLabels)
			flags = optRepeated(flags, "--rm-label", in.RmLabels)
			flags = anchorEditFlags(flags, in.anchorEditArgs)
			return b.run(ctx, argvFor([]string{"ledger", "edit"}, flags, in.ID)...)
		})

	idTool(ts, b, "ledger_rm", "Tombstone a ledger.", "ledger", "rm")

	addTool(ts, &mcp.Tool{Name: "ledger_search", Description: "Ranked search across ledger titles, labels, descriptions, row keys, and field values. Returns summaries carrying the row tally; ledger_show reads the rows back."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ledgerSearchArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optInt(flags, "--limit", in.Limit)
			flags = optStr(flags, "--author", in.Author)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			return b.run(ctx, argvFor([]string{"ledger", "search"}, flags, in.Query)...)
		})

	commentTool(ts, b, "ledger")

	addTool(ts, &mcp.Tool{Name: "ledger_sync", Description: "Write a whole row set in one commit — the refresh path. Each row's fields merge into the row already keyed by it, so fields this call does not name keep their value and an operator's hold reason survives a refresh; rows the ledger has never seen are appended. Set prune to remove every row the set omits, which is how a unit that left the external system leaves the ledger. The ack reports what the pass added, updated, and removed."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ledgerSyncArgs) (*mcp.CallToolResult, any, error) {
			path, cleanup, err := writeRowsFile(in.Rows)
			if err != nil {
				return nil, nil, err
			}
			defer cleanup()
			flags := []string{"--json", "--file", path}
			flags = optBool(flags, "--prune", in.Prune)
			return b.run(ctx, argvFor([]string{"ledger", "sync"}, flags, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "ledger_row_set", Description: "Write fields into one row, inserting it when the ledger has none. Fields this call does not name keep their value unless replace is set."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ledgerRowSetArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json", "--key", in.Key}
			flags = append(flags, fieldFlags("--field", in.Fields)...)
			flags = optBool(flags, "--replace", in.Replace)
			return b.run(ctx, argvFor([]string{"ledger", "row", "set"}, flags, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "ledger_row_rm", Description: "Remove one row from a ledger."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ledgerRowRefArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"ledger", "row", "rm"}, []string{"--json", "--key", in.Key}, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "ledger_row_list", Description: "List a ledger's rows, narrowed to the rows whose fields hold every where pair."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ledgerRowListArgs) (*mcp.CallToolResult, any, error) {
			flags := append([]string{"--json"}, fieldFlags("--where", in.Where)...)
			return b.run(ctx, argvFor([]string{"ledger", "row", "list"}, flags, in.ID)...)
		})
}

// fieldFlags renders a field map as repeated NAME=VALUE flags in sorted name
// order, so one call's argv is stable across runs.
func fieldFlags(flag string, fields map[string]string) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	argv := make([]string, 0, 2*len(names))
	for _, name := range names {
		argv = append(argv, flag, name+"="+fields[name])
	}
	return argv
}

// writeRowsFile spills a sync's rows to a temp file for the CLI's --file, since
// the bridge runs the CLI over argv and a large row set does not belong there.
func writeRowsFile(rows []ledgerRowArg) (string, func(), error) {
	f, err := os.CreateTemp("", "ccn-ledger-rows-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("stage rows: %w", err)
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if err := json.NewEncoder(f).Encode(rows); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("stage rows: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("stage rows: %w", err)
	}
	return f.Name(), cleanup, nil
}
