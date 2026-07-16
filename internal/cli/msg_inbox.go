package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	msgInboxWait     bool
	msgInboxFollow   bool
	msgInboxAck      bool
	msgInboxAll      bool
	msgInboxThreadID string
)

var msgInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Read your inbox messages",
	Long:  "Read messages from the authenticated session's inbox. Requires GRAITH_TOKEN (set automatically inside graith sessions).",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("msg_inbox", protocol.MsgInboxMsg{
			OnlyUnread: !msgInboxAll,
			ThreadID:   msgInboxThreadID,
			Wait:       msgInboxWait,
			Follow:     msgInboxFollow,
			Ack:        msgInboxAck,
		})

		if msgInboxFollow || msgInboxWait {
			sigCh := make(chan os.Signal, 1)

			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh

				_ = c.SendControl("detach", struct{}{})
			}()
		}

		return readMessageStream(c)
	},
}
