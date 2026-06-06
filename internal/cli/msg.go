package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/dougalmatthews/graith/internal/client"
	"github.com/dougalmatthews/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var msgCmd = &cobra.Command{
	Use:   "msg",
	Short: "Inter-agent messaging",
}

// --- gr msg pub ---

var (
	msgPubStream   string
	msgPubFile     string
	msgPubThreadID string
	msgPubReplyTo  string
)

var msgPubCmd = &cobra.Command{
	Use:   "pub <body>",
	Short: "Publish a message to a stream",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := resolveBody(args, msgPubFile)
		if err != nil {
			return err
		}
		if msgPubStream == "" {
			return fmt.Errorf("--topic is required")
		}

		senderID, senderName := detectSender()

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("msg_pub", protocol.MsgPubMsg{
			Stream:     msgPubStream,
			Body:       body,
			SenderID:   senderID,
			SenderName: senderName,
			ThreadID:   msgPubThreadID,
			ReplyTo:    msgPubReplyTo,
		})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		if jsonOutput {
			return out.JSON(json.RawMessage(resp.Payload))
		}
		out.Print("Published to %s\n", msgPubStream)
		return nil
	},
}

// --- gr msg send ---

var (
	msgSendFile     string
	msgSendThreadID string
	msgSendReplyTo  string
)

var msgSendCmd = &cobra.Command{
	Use:   "send <session-name-or-id> <body>",
	Short: "Send a message to a session's inbox",
	Args:  cobra.RangeArgs(1, 2),
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

		bodyArgs := args[1:]
		body, err := resolveBody(bodyArgs, msgSendFile)
		if err != nil {
			return err
		}

		senderID, senderName := detectSender()

		c.SendControl("msg_pub", protocol.MsgPubMsg{
			Stream:     "inbox:" + sessionID,
			Body:       body,
			SenderID:   senderID,
			SenderName: senderName,
			ThreadID:   msgSendThreadID,
			ReplyTo:    msgSendReplyTo,
		})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		if jsonOutput {
			return out.JSON(json.RawMessage(resp.Payload))
		}
		out.Print("Sent to inbox:%s\n", sessionID)
		return nil
	},
}

// --- gr msg sub ---

var (
	msgSubStream   string
	msgSubWait     bool
	msgSubFollow   bool
	msgSubAck      bool
	msgSubAll      bool
	msgSubThreadID string
)

var msgSubCmd = &cobra.Command{
	Use:   "sub",
	Short: "Read messages from a stream",
	RunE: func(cmd *cobra.Command, args []string) error {
		if msgSubStream == "" {
			return fmt.Errorf("--topic is required")
		}

		senderID, _ := detectSender()

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("msg_sub", protocol.MsgSubMsg{
			Stream:     msgSubStream,
			Subscriber: senderID,
			OnlyUnread: !msgSubAll,
			ThreadID:   msgSubThreadID,
			Wait:       msgSubWait,
			Follow:     msgSubFollow,
			Ack:        msgSubAck,
		})

		if msgSubFollow || msgSubWait {
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
			if frame.Channel != protocol.ChannelControl {
				continue
			}
			msg, _ := protocol.DecodeControl(frame.Payload)
			switch msg.Type {
			case "msg_message":
				if jsonOutput {
					fmt.Println(string(msg.Payload))
				} else {
					printMessage(msg.Payload)
				}
			case "msg_done":
				return nil
			case "msg_following":
				// streaming mode active, keep reading
			case "error":
				var e protocol.ErrorMsg
				protocol.DecodePayload(msg, &e)
				return fmt.Errorf("%s", e.Message)
			}
		}
	},
}

// --- gr msg ack ---

var msgAckStream string

var msgAckCmd = &cobra.Command{
	Use:   "ack",
	Short: "Acknowledge all messages in a stream",
	RunE: func(cmd *cobra.Command, args []string) error {
		if msgAckStream == "" {
			return fmt.Errorf("--topic is required")
		}

		senderID, _ := detectSender()

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("msg_ack", protocol.MsgAckMsg{
			Stream:     msgAckStream,
			Subscriber: senderID,
		})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}
		out.Print("Acknowledged messages in %s\n", msgAckStream)
		return nil
	},
}

// --- gr msg topics ---

var msgTopicsCmd = &cobra.Command{
	Use:   "topics",
	Short: "List streams with message counts",
	RunE: func(cmd *cobra.Command, args []string) error {
		senderID, _ := detectSender()

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("msg_topics", protocol.MsgTopicsMsg{
			Subscriber: senderID,
		})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		var list struct {
			Streams []struct {
				Name     string `json:"name"`
				Total    int64  `json:"total"`
				Unread   int64  `json:"unread"`
				LatestAt string `json:"latest_at"`
			} `json:"streams"`
		}
		protocol.DecodePayload(resp, &list)

		if jsonOutput {
			return out.JSON(list)
		}

		if len(list.Streams) == 0 {
			out.Print("No messages.\n")
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "STREAM\tTOTAL\tUNREAD\tLATEST")
		for _, s := range list.Streams {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", s.Name, s.Total, s.Unread, s.LatestAt)
		}
		tw.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(msgCmd)

	msgCmd.AddCommand(msgPubCmd)
	msgPubCmd.Flags().StringVarP(&msgPubStream, "topic", "t", "", "stream/topic name")
	msgPubCmd.Flags().StringVarP(&msgPubFile, "file", "f", "", "read body from file")
	msgPubCmd.Flags().StringVar(&msgPubThreadID, "thread", "", "thread ID to continue")
	msgPubCmd.Flags().StringVar(&msgPubReplyTo, "reply-to", "", "stream for replies")

	msgCmd.AddCommand(msgSendCmd)
	msgSendCmd.Flags().StringVarP(&msgSendFile, "file", "f", "", "read body from file")
	msgSendCmd.Flags().StringVar(&msgSendThreadID, "thread", "", "thread ID to continue")
	msgSendCmd.Flags().StringVar(&msgSendReplyTo, "reply-to", "", "stream for replies")

	msgCmd.AddCommand(msgSubCmd)
	msgSubCmd.Flags().StringVarP(&msgSubStream, "topic", "t", "", "stream/topic name")
	msgSubCmd.Flags().BoolVarP(&msgSubWait, "wait", "w", false, "block until a message arrives")
	msgSubCmd.Flags().BoolVarP(&msgSubFollow, "follow", "F", false, "stream new messages continuously")
	msgSubCmd.Flags().BoolVar(&msgSubAck, "ack", false, "acknowledge messages after reading")
	msgSubCmd.Flags().BoolVarP(&msgSubAll, "all", "a", false, "show all messages, not just unread")
	msgSubCmd.Flags().StringVar(&msgSubThreadID, "thread", "", "filter to a specific thread")

	msgCmd.AddCommand(msgAckCmd)
	msgAckCmd.Flags().StringVarP(&msgAckStream, "topic", "t", "", "stream/topic name")

	msgCmd.AddCommand(msgTopicsCmd)
}

func detectSender() (id, name string) {
	id = os.Getenv("GRAITH_SESSION_ID")
	name = os.Getenv("GRAITH_SESSION_NAME")
	if id == "" {
		id = fmt.Sprintf("pid:%d", os.Getpid())
	}
	return id, name
}

func resolveBody(args []string, filePath string) (string, error) {
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read body file: %w", err)
		}
		return string(data), nil
	}
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), nil
	}
	return "", fmt.Errorf("message body required (as argument, --file, or stdin)")
}

func resolveSession(c *client.Client, nameOrID string) (string, error) {
	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return "", err
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return "", err
	}
	for _, s := range list.Sessions {
		if s.Name == nameOrID || s.ID == nameOrID {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("session %q not found", nameOrID)
}

func printMessage(payload json.RawMessage) {
	var m struct {
		ID         string `json:"id"`
		Seq        int64  `json:"seq"`
		SenderName string `json:"sender_name"`
		SenderID   string `json:"sender_id"`
		Body       string `json:"body"`
		CreatedAt  string `json:"created_at"`
		ThreadID   string `json:"thread_id"`
	}
	json.Unmarshal(payload, &m)

	sender := m.SenderName
	if sender == "" {
		sender = m.SenderID
	}

	threadInfo := ""
	if m.ThreadID != "" {
		threadInfo = fmt.Sprintf(" [thread:%s]", m.ThreadID[:min(12, len(m.ThreadID))])
	}

	fmt.Printf("[%s] #%d %s%s:\n%s\n\n", m.CreatedAt, m.Seq, sender, threadInfo, m.Body)
}
