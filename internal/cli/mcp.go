package cli

import (
	"context"
	"os/signal"
	"syscall"

	graithmcp "github.com/d0ugal/graith/internal/mcp"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run graith as an MCP tool server (stdio transport)",
	Long: `Start an MCP (Model Context Protocol) server that exposes graith
session management as tools. The server communicates over stdin/stdout
using the MCP stdio transport.

Tools exposed:
  list_sessions     List all sessions
  session_status    Get status of a specific session
  create_session    Create a new session
  publish_message   Publish a message to a topic
  read_inbox        Read messages from this session's inbox
  read_messages     Read messages from a topic (not inboxes)
  subscribe         Wait for the next message on a topic (not inboxes)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		srv := graithmcp.NewServer(cfg, paths, cfgFile)

		return srv.Run(ctx)
	},
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
