package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
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
		socketPath := os.Getenv("GRAITH_SOCKET_PATH")

		return runMCPProxy(serverName, sessionID, socketPath)
	},
}

// stdinChunk carries data read from os.Stdin.
type stdinChunk struct {
	data []byte
	err  error
}

func runMCPProxy(serverName, sessionID, socketPath string) error {
	const maxBackoff = 30 * time.Second

	proxyPaths, err := mcpProxyConnectionPaths(paths, socketPath)
	if err != nil {
		return err
	}

	backoff := time.Second

	// Single long-lived goroutine reading stdin. Shared across reconnect
	// cycles to prevent goroutine leaks (os.Stdin.Read blocks forever and
	// cannot be cancelled).
	stdinCh := make(chan stdinChunk, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])

				stdinCh <- stdinChunk{data: cp}
			}

			if err != nil {
				stdinCh <- stdinChunk{err: err}
				return
			}
		}
	}()

	for {
		start := time.Now()

		err := mcpProxySession(serverName, sessionID, proxyPaths, stdinCh)
		if err == nil {
			return nil
		}

		if isPermanentError(err) {
			return err
		}

		// Emit JSON-RPC error so agents don't hang waiting for a response.
		writeJSONRPCError(os.Stdout, nil, -32603, fmt.Sprintf("MCP server %q temporarily unavailable", serverName))
		fmt.Fprintf(os.Stderr, "mcp-proxy: connection lost: %v, reconnecting in %s\n", err, backoff)

		time.Sleep(backoff)

		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		} else {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func isPermanentError(err error) bool {
	msg := err.Error()

	return strings.Contains(msg, "unknown MCP server") ||
		strings.Contains(msg, "MCP manager not initialized") ||
		strings.Contains(msg, "is not enabled for agent") ||
		strings.Contains(msg, "requires an authenticated session identity")
}

func mcpProxyConnectionPaths(base config.Paths, socketPath string) (config.Paths, error) {
	if socketPath == "" {
		return base, nil
	}

	if !filepath.IsAbs(socketPath) {
		return config.Paths{}, fmt.Errorf("GRAITH_SOCKET_PATH must be absolute")
	}

	base.SocketPath = filepath.Clean(socketPath)

	return base, nil
}

func mcpProxySession(serverName, sessionID string, proxyPaths config.Paths, stdinCh <-chan stdinChunk) error {
	// A proxy is owned by an already-running daemon. Never auto-start a daemon
	// from this credential-bearing helper: on restart the outer retry loop waits
	// for the exact socket to return.
	c, err := client.ConnectExisting(cfg, proxyPaths)
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

		_ = protocol.DecodePayload(resp, &e)
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

	// Bridge: stdinCh → daemon (as MCP channel frames),
	//         daemon MCP channel frames → stdout.
	daemonDone := make(chan error, 1)

	// daemon → stdout
	go func() {
		for {
			frame, err := c.ReadFrame()
			if err != nil {
				daemonDone <- err
				return
			}

			switch frame.Channel {
			case mcpChannel:
				if _, werr := os.Stdout.Write(frame.Payload); werr != nil {
					daemonDone <- werr
					return
				}
			case protocol.ChannelControl:
				ctrl, _ := protocol.DecodeControl(frame.Payload)
				if ctrl.Type == "error" {
					var e protocol.ErrorMsg

					_ = protocol.DecodePayload(ctrl, &e)

					daemonDone <- fmt.Errorf("server error: %s", e.Message)

					return
				}
			}
		}
	}()

	// stdin → daemon (uses shared stdinCh, no new goroutine per session)
	for {
		select {
		case chunk := <-stdinCh:
			if chunk.err != nil {
				if chunk.err == io.EOF {
					return nil
				}

				return chunk.err
			}

			if err := c.SendFrame(mcpChannel, chunk.data); err != nil {
				return err
			}
		case err := <-daemonDone:
			if err == io.EOF {
				return errors.New("daemon connection closed")
			}

			return err
		}
	}
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

	data, err := json.Marshal(resp)
	if err != nil {
		return
	}

	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n"))
}

// registerMCPProxyCmd registers this command on rootCmd. Called from registerCommands.
func registerMCPProxyCmd() {
	rootCmd.AddCommand(mcpProxyCmd)
}
