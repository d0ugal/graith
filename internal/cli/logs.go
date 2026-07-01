package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	logsFollow bool
	logsLines  int
)

var logsCmd = &cobra.Command{
	Use:               "logs <name-or-id>",
	Aliases:           []string{"l"},
	Short:             "Show session output without attaching",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		sessionID, err := resolveSession(c, args[0])
		if err != nil {
			return err
		}

		c.SendControl("logs", protocol.LogsMsg{
			SessionID: sessionID,
			Lines:     logsLines,
			Follow:    logsFollow,
		})

		if logsFollow {
			sigCh := make(chan os.Signal, 1)

			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				c.SendControl("detach", struct{}{})
			}()
		}

		for {
			frame, err := c.ReadFrame()
			if err != nil {
				if err == io.EOF {
					return nil
				}

				return err
			}

			switch frame.Channel {
			case protocol.ChannelData:
				os.Stdout.Write(frame.Payload)
			case protocol.ChannelControl:
				msg, _ := protocol.DecodeControl(frame.Payload)
				if msg.Type == "logs_done" || msg.Type == "error" {
					if msg.Type == "error" {
						var e protocol.ErrorMsg
						protocol.DecodePayload(msg, &e)

						return fmt.Errorf("%s", e.Message)
					}

					return nil
				}
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "follow output (like tail -f)")
	logsCmd.Flags().IntVarP(&logsLines, "lines", "n", 300, "number of lines to show")
}
