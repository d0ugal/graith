package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

// inboxReadTimeout bounds how long the check-inbox hook waits for the daemon to
// finish streaming inbox frames. Without it a slow or hung daemon connection
// would block the agent's SessionStart hook indefinitely.
const inboxReadTimeout = 5 * time.Second

// frameReader is the subset of *client.Client used to read the inbox response.
// It exists so the read loop can be unit-tested without a live daemon.
type frameReader interface {
	ReadFrame() (protocol.Frame, error)
}

var checkInboxCmd = &cobra.Command{
	Use:    "check-inbox",
	Short:  "Check for unread inbox messages and output systemMessage (used by hooks)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		sessionID := os.Getenv("GRAITH_SESSION_ID")
		if sessionID == "" {
			return nil
		}

		hookPaths, err := config.ResolvePaths()
		if err != nil {
			return nil
		}

		c, err := client.ConnectFast(hookPaths)
		if err != nil {
			return nil
		}
		defer c.Close()

		_ = c.SendControl("msg_inbox", protocol.MsgInboxMsg{
			OnlyUnread: true,
			Ack:        true,
		})

		// Bound the read so a slow or hung daemon can't block the hook forever.
		_ = c.SetReadDeadline(time.Now().Add(inboxReadTimeout))

		messages, err := readInboxMessages(c)
		if err != nil {
			// Don't fail the agent's hook, but don't swallow the error either:
			// surface it on stderr and emit whatever we managed to collect.
			fmt.Fprintf(os.Stderr, "gr check-inbox: %v\n", err)
		}

		if len(messages) == 0 {
			return nil
		}

		var preview strings.Builder

		for _, m := range messages {
			sender := m.SenderName
			if sender == "" {
				sender = m.SenderID
			}

			if m.System {
				fmt.Fprintf(&preview, "Automated notification from %s: %s\n", sender, m.Body)
			} else {
				fmt.Fprintf(&preview, "From %s: %s\n", sender, m.Body)
			}
		}

		previewStr := preview.String()
		if len(previewStr) > 1000 {
			previewStr = previewStr[:1000] + "..."
		}

		systemMsg := fmt.Sprintf(
			"You have %d unread message(s) in your graith inbox. Read with: gr msg inbox --all\n\n%s",
			len(messages), previewStr,
		)

		out, _ := json.Marshal(map[string]string{
			"systemMessage": systemMsg,
		})
		fmt.Println(string(out))

		return nil
	},
}

// readInboxMessages reads control frames from fr until it sees msg_done, an
// error frame, EOF, or a read error (including a deadline timeout). Messages
// collected before a terminating error are returned alongside that error, so
// the caller can still surface what arrived. A frame that fails to decode is
// reported rather than silently swallowed: the old code ignored the decode
// error, yielding an empty envelope that matched no case and left the loop
// waiting for a msg_done that was already lost.
func readInboxMessages(fr frameReader) ([]inboxMessage, error) {
	var messages []inboxMessage

	for {
		frame, err := fr.ReadFrame()
		if err != nil {
			// Only a bare io.EOF means the daemon closed cleanly at a frame
			// boundary. A wrapped EOF (e.g. "read frame payload: EOF" from a
			// truncated frame) is a real error and must be surfaced, not
			// mistaken for a clean end of stream.
			if err == io.EOF {
				return messages, nil
			}

			return messages, err
		}

		if frame.Channel != protocol.ChannelControl {
			continue
		}

		msg, err := protocol.DecodeControl(frame.Payload)
		if err != nil {
			return messages, fmt.Errorf("decode inbox frame: %w", err)
		}

		switch msg.Type {
		case "msg_message":
			var m inboxMessage
			if json.Unmarshal(msg.Payload, &m) == nil {
				messages = append(messages, m)
			}
		case "msg_done":
			return messages, nil
		case "error":
			var e protocol.ErrorMsg
			if json.Unmarshal(msg.Payload, &e) == nil && e.Message != "" {
				return messages, fmt.Errorf("daemon error while reading inbox: %s", e.Message)
			}

			return messages, fmt.Errorf("daemon returned an error while reading inbox")
		}
	}
}

type inboxMessage struct {
	ID         string `json:"id"`
	SenderName string `json:"sender_name"`
	SenderID   string `json:"sender_id"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
	System     bool   `json:"system"`
}

// registerCheckInboxCmd registers this command on rootCmd. Called from registerCommands.
func registerCheckInboxCmd() {
	rootCmd.AddCommand(checkInboxCmd)
}
