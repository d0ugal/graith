package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var mcpProxyCmd = &cobra.Command{
	Use:    "mcp-proxy <server-name>",
	Short:  "Stdio bridge to a daemon-managed MCP server",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		serverName := args[0]
		sessionID := os.Getenv("GRAITH_SESSION_ID")

		return runMCPProxy(serverName, sessionID)
	},
}

func runMCPProxy(serverName, sessionID string) error {
	const maxBackoff = 30 * time.Second
	backoff := time.Second

	for {
		err := mcpProxySession(serverName, sessionID)
		if err == nil {
			return nil
		}

		// Return a JSON-RPC error on stderr so agents can see what happened.
		fmt.Fprintf(os.Stderr, "mcp-proxy: connection lost: %v, reconnecting in %s\n", err, backoff)
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func mcpProxySession(serverName, sessionID string) error {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer c.Close()

	if err := c.SendControl("mcp_connect", protocol.MCPConnectMsg{
		Server:    serverName,
		SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("send mcp_connect: %w", err)
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return fmt.Errorf("read mcp_connect response: %w", err)
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)
		// Non-retriable errors: unknown server, etc.
		writeJSONRPCError(os.Stdout, nil, -32603, fmt.Sprintf("MCP server %q: %s", serverName, e.Message))
		return fmt.Errorf("%s", e.Message)
	}

	if resp.Type != "mcp_connect_ok" {
		return fmt.Errorf("unexpected response: %s", resp.Type)
	}

	var connectOk protocol.MCPConnectOkMsg
	if err := protocol.DecodePayload(resp, &connectOk); err != nil {
		return fmt.Errorf("decode mcp_connect_ok: %w", err)
	}

	mcpChannel := connectOk.Channel

	// Bridge: stdin → daemon (as MCP channel frames),
	//         daemon MCP channel frames → stdout.
	done := make(chan error, 2)

	// stdin → daemon
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if serr := c.SendFrame(mcpChannel, buf[:n]); serr != nil {
					done <- serr
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()

	// daemon → stdout
	go func() {
		for {
			frame, err := c.ReadFrame()
			if err != nil {
				done <- err
				return
			}
			if frame.Channel == mcpChannel {
				if _, werr := os.Stdout.Write(frame.Payload); werr != nil {
					done <- werr
					return
				}
			} else if frame.Channel == protocol.ChannelControl {
				ctrl, _ := protocol.DecodeControl(frame.Payload)
				if ctrl.Type == "error" {
					var e protocol.ErrorMsg
					protocol.DecodePayload(ctrl, &e)
					done <- fmt.Errorf("server error: %s", e.Message)
					return
				}
			}
		}
	}()

	err = <-done
	if err == io.EOF {
		return nil
	}
	return err
}

func writeJSONRPCError(w io.Writer, id any, code int, message string) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	data, _ := json.Marshal(resp)
	w.Write(data)
	w.Write([]byte("\n"))
}

func init() {
	rootCmd.AddCommand(mcpProxyCmd)
}
