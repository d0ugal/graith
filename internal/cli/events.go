package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Stream session lifecycle and public message events",
	Args:  cobra.NoArgs,
	RunE:  runEvents,
}

func runEvents(cmd *cobra.Command, _ []string) error {
	c, err := client.ConnectPassive(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.SendControl("events_sub", protocol.EventsSubMsg{}); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)

	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	go func() {
		<-sigCh

		_ = c.SendControl("detach", struct{}{})
	}()

	return readEventStream(c, cmd.OutOrStdout(), jsonOutput)
}

func readEventStream(c *client.Client, w io.Writer, jsonMode bool) error {
	for {
		frame, err := c.ReadFrame()
		if err != nil {
			if err == io.EOF {
				return nil
			}

			return err
		}

		if frame.Channel != protocol.ChannelControl {
			continue
		}

		msg, _ := protocol.DecodeControl(frame.Payload)
		switch msg.Type {
		case "event":
			if jsonMode {
				_, _ = fmt.Fprintln(w, string(msg.Payload))
			} else {
				if err := printEvent(w, msg.Payload); err != nil {
					return err
				}
			}
		case "events_following":
			// streaming mode active, keep reading
		case "events_done":
			return nil
		case "error":
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(msg, &e)

			return fmt.Errorf("%s", e.Message)
		}
	}
}

func printEvent(w io.Writer, payload json.RawMessage) error {
	var ev protocol.EventMsg
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("decode event: %w", err)
	}

	session := ev.Session
	if session == "" {
		session = ev.SessionID
	}

	switch ev.Type {
	case "status_change":
		kind := ev.StatusKind
		if kind != "" {
			kind += " "
		}

		_, _ = fmt.Fprintf(w, "[%s] %s %sstatus: %s -> %s\n", ev.At, session, kind, ev.From, ev.To)
	case "message":
		sender := ev.Sender
		if sender == "" {
			sender = ev.SenderID
		}

		_, _ = fmt.Fprintf(w, "[%s] message %s from %s: %s\n", ev.At, ev.Topic, sender, ev.Body)
	case "session_deleted":
		_, _ = fmt.Fprintf(w, "[%s] session deleted: %s\n", ev.At, session)
	default:
		_, _ = fmt.Fprintf(w, "[%s] %s\n", ev.At, ev.Type)
	}

	return nil
}

// registerEventsCmd registers this command on rootCmd. Called from
// registerCommands.
func registerEventsCmd() {
	rootCmd.AddCommand(eventsCmd)
}
