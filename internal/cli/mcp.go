package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	graithmcp "github.com/d0ugal/graith/internal/mcp"
	"github.com/d0ugal/graith/internal/protocol"
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

var mcpLogsLines int

var mcpListCmd = &cobra.Command{
	Use:   "list",
	Short: "List daemon-managed MCP servers and their status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := cliRequest("mcp_list", protocol.MCPListMsg{})
		if err != nil {
			return err
		}

		var listResp protocol.MCPListResponse

		_ = protocol.DecodePayload(resp, &listResp)

		if jsonOutput {
			return out.JSON(listResp)
		}

		renderMCPList(os.Stdout, listResp.Servers)

		return nil
	},
}

var mcpRestartCmd = &cobra.Command{
	Use:   "restart <name>",
	Short: "Restart a daemon-managed MCP server",
	Long: "Stop all running processes for the named MCP server. Agent proxies " +
		"reconnect automatically and the daemon starts fresh processes with the " +
		"current config.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := cliRequest("mcp_restart", protocol.MCPRestartMsg{Name: args[0]})
		if err != nil {
			return err
		}

		var r protocol.MCPRestartResponse

		_ = protocol.DecodePayload(resp, &r)

		if jsonOutput {
			return out.JSON(r)
		}

		if r.Stopped == 0 {
			out.Printf("MCP server %q had no running processes; new connections will start fresh.\n", r.Name)
		} else {
			out.Printf("Restarted MCP server %q (stopped %d process(es); proxies will reconnect).\n", r.Name, r.Stopped)
		}

		return nil
	},
}

var mcpLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show captured stderr for a daemon-managed MCP server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := cliRequest("mcp_logs", protocol.MCPLogsMsg{Name: args[0], Lines: mcpLogsLines})
		if err != nil {
			return err
		}

		var logsResp protocol.MCPLogsResponse

		_ = protocol.DecodePayload(resp, &logsResp)

		if jsonOutput {
			return out.JSON(logsResp)
		}

		renderMCPLogs(os.Stdout, logsResp)

		return nil
	},
}

// renderMCPList writes the human-readable MCP server table.
func renderMCPList(w io.Writer, servers []protocol.MCPServerStatus) {
	if len(servers) == 0 {
		_, _ = fmt.Fprintln(w, "No MCP servers configured.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSANDBOX\tSOURCE\tCONNECTIONS\tUPTIME")

	for _, s := range servers {
		sandbox := "on"
		if !s.Sandboxed {
			sandbox = "off"
		}

		source := "config"
		if s.AutoInjected {
			source = "auto"
		}

		uptime := "-"

		if len(s.Connections) > 0 {
			// Report the longest-running connection's uptime.
			longest := s.Connections[0].UptimeSec
			uptime = s.Connections[0].Uptime

			for _, c := range s.Connections[1:] {
				if c.UptimeSec > longest {
					longest = c.UptimeSec
					uptime = c.Uptime
				}
			}
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			s.Name, sandbox, source, len(s.Connections), uptime)
	}

	_ = tw.Flush()
}

// renderMCPLogs writes the captured stderr for each proxy of an MCP server.
func renderMCPLogs(w io.Writer, resp protocol.MCPLogsResponse) {
	if len(resp.Files) == 0 {
		_, _ = fmt.Fprintf(w, "No captured logs for MCP server %q.\n", resp.Name)
		return
	}

	for i, f := range resp.Files {
		if len(resp.Files) > 1 {
			if i > 0 {
				_, _ = fmt.Fprintln(w)
			}

			_, _ = fmt.Fprintf(w, "==> %s <==\n", f.ProxyID)
		}

		_, _ = fmt.Fprint(w, f.Content)
	}
}

// registerMCPCmd registers this command on rootCmd. Called from registerCommands.
func registerMCPCmd() {
	mcpLogsCmd.Flags().IntVarP(&mcpLogsLines, "lines", "n", 300, "number of lines to show per log")
	mcpCmd.AddCommand(mcpListCmd)
	mcpCmd.AddCommand(mcpRestartCmd)
	mcpCmd.AddCommand(mcpLogsCmd)
	rootCmd.AddCommand(mcpCmd)
}
