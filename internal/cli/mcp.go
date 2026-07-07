package cli

import (
	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/mcpserver"
	"github.com/yasyf/cc-notes/internal/version"
)

func newMCPCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the cc-notes MCP server over stdio",
		Long: "Serve the cc-notes tool surface to an MCP client (e.g. Claude Code) over\n" +
			"stdio. Each tool drives the same command as the CLI in-process, returning\n" +
			"the JSON DTO as the result.",
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return mcpserver.Serve(cmd.Context(), dir, mcpserver.Config{
				Version: version.String(),
				NewRoot: NewRootCmd,
				Label:   Label,
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "project directory (default: $CLAUDE_PROJECT_DIR or cwd)")
	return cmd
}
