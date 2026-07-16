package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	waitContains string
	waitStatus   string
	waitIdle     bool
	waitTimeout  time.Duration
)

// errWaitTimeout is returned when a wait condition is not met before the
// timeout. It yields a non-zero exit code, distinct from a match (exit 0).
var errWaitTimeout = errors.New("timed out waiting for condition")

var waitCmd = &cobra.Command{
	Use:   "wait <name-or-id>",
	Short: "Block until a session's output or status matches a condition",
	Long: `Block until a session satisfies a condition, then exit.

Exactly one condition must be given:

  --contains <regex>   return when a line of the session's output matches
  --status <status>    return when the session reaches a lifecycle status
                       (e.g. running, stopped)
  --idle               return when the session's agent becomes idle

With --timeout, the command exits non-zero if the condition is not met in
time; otherwise it waits indefinitely. It exits 0 as soon as the condition
is satisfied.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		mode, pattern, err := resolveWaitMode()
		if err != nil {
			return err
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		sessionID, err := resolveSession(c, args[0])
		if err != nil {
			return err
		}

		if err := c.SendControl("wait", protocol.WaitMsg{
			SessionID: sessionID,
			Mode:      mode,
			Pattern:   pattern,
			Status:    waitStatus,
			TimeoutMs: timeoutMillis(waitTimeout),
		}); err != nil {
			return err
		}

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		go func() {
			<-sigCh

			_ = c.SendControl("detach", struct{}{})
		}()

		return readWaitResult(c)
	},
}

// validWaitStatuses are the session lifecycle statuses --status accepts. Kept
// in sync with the daemon's SessionStatus constants.
var validWaitStatuses = map[string]bool{
	"running": true, "stopped": true, "errored": true,
	"creating": true, "deleting": true,
}

// resolveWaitMode validates the flag combination and returns the wire mode plus
// the (validated) contains pattern.
func resolveWaitMode() (mode, pattern string, err error) {
	n := 0
	if waitContains != "" {
		n++
		mode = "contains"
	}

	if waitStatus != "" {
		n++
		mode = "status"
	}

	if waitIdle {
		n++
		mode = "idle"
	}

	switch {
	case n == 0:
		return "", "", errors.New("one of --contains, --status, or --idle is required")
	case n > 1:
		return "", "", errors.New("--contains, --status, and --idle are mutually exclusive")
	}

	if waitTimeout < 0 {
		return "", "", errors.New("--timeout must not be negative")
	}

	switch mode {
	case "contains":
		if _, err := regexp.Compile(waitContains); err != nil {
			return "", "", fmt.Errorf("invalid --contains pattern: %w", err)
		}
	case "status":
		if !validWaitStatuses[waitStatus] {
			return "", "", fmt.Errorf("invalid --status %q: want one of running, stopped, errored, creating, deleting", waitStatus)
		}
	}

	return mode, waitContains, nil
}

// timeoutMillis converts a wait timeout to whole milliseconds for the wire.
// A positive duration below 1ms is floored to 1ms so it stays a real (short)
// timeout rather than truncating to 0, which the daemon reads as "wait forever".
func timeoutMillis(d time.Duration) int {
	if d <= 0 {
		return 0
	}

	if ms := d.Milliseconds(); ms > 0 {
		return int(ms)
	}

	return 1
}

// readWaitResult reads control responses until the wait resolves. It returns
// nil on a match, errWaitTimeout on timeout, and the daemon's error otherwise.
func readWaitResult(c *client.Client) error {
	for {
		frame, err := c.ReadFrame()
		if err != nil {
			if err == io.EOF {
				return errors.New("connection closed before condition was met")
			}

			return err
		}

		if frame.Channel != protocol.ChannelControl {
			continue
		}

		msg, err := protocol.DecodeControl(frame.Payload)
		if err != nil {
			continue
		}

		switch msg.Type {
		case "wait_matched":
			var m protocol.WaitMatchedMsg

			_ = protocol.DecodePayload(msg, &m)

			reportWaitMatched(m)

			return nil
		case "wait_timeout":
			return errWaitTimeout
		case "error":
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(msg, &e)

			return fmt.Errorf("%s", e.Message)
		}
	}
}

func reportWaitMatched(m protocol.WaitMatchedMsg) {
	if out.IsJSON() {
		_ = out.JSON(struct {
			Matched     bool   `json:"matched"`
			MatchedLine string `json:"matched_line,omitempty"`
			Status      string `json:"status,omitempty"`
		}{Matched: true, MatchedLine: m.MatchedLine, Status: m.Status})

		return
	}

	switch {
	case m.MatchedLine != "":
		out.Printf("matched: %s\n", m.MatchedLine)
	case m.Status != "":
		out.Printf("reached status: %s\n", m.Status)
	default:
		out.Printf("condition met\n")
	}
}

// registerWaitCmd registers this command on rootCmd. Called from registerCommands.
func registerWaitCmd() {
	waitCmd.Flags().StringVar(&waitContains, "contains", "", "return when output matches this regexp")
	waitCmd.Flags().StringVar(&waitStatus, "status", "", "return when the session reaches this status (e.g. running, stopped)")
	waitCmd.Flags().BoolVar(&waitIdle, "idle", false, "return when the session's agent becomes idle")
	waitCmd.Flags().DurationVar(&waitTimeout, "timeout", 0, "give up after this duration (0 = wait forever)")
	rootCmd.AddCommand(waitCmd)
}
