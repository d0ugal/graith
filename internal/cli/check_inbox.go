package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

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

		var messages []inboxMessage

		for {
			frame, err := c.ReadFrame()
			if err != nil {
				if err == io.EOF {
					break
				}

				return nil
			}

			if frame.Channel != protocol.ChannelControl {
				continue
			}

			msg, _ := protocol.DecodeControl(frame.Payload)
			switch msg.Type {
			case "msg_message":
				var m inboxMessage
				if json.Unmarshal(msg.Payload, &m) == nil {
					messages = append(messages, m)
				}
			case "msg_done":
				goto done
			case "error":
				return nil
			}
		}

	done:

		if len(messages) == 0 {
			return nil
		}

		var preview strings.Builder

		for _, m := range messages {
			sender := m.SenderName
			if sender == "" {
				sender = m.SenderID
			}

			fmt.Fprintf(&preview, "From %s: %s\n", sender, m.Body)
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

type inboxMessage struct {
	ID         string `json:"id"`
	SenderName string `json:"sender_name"`
	SenderID   string `json:"sender_id"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
}

// registerCheckInboxCmd registers this command on rootCmd. Called from registerCommands.
func registerCheckInboxCmd() {
	rootCmd.AddCommand(checkInboxCmd)
}
