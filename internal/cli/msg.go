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

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var msgCmd = &cobra.Command{
	Use:     "msg",
	Aliases: []string{"m"},
	Short:   "Inter-agent messaging",
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
	msgSendQuiet    bool
	msgSendChildren bool
	msgSendParent   bool
)

var msgSendCmd = &cobra.Command{
	Use:   "send <session-name-or-id> <body>",
	Short: "Send a message to a session's inbox",
	Args: func(cmd *cobra.Command, args []string) error {
		if msgSendChildren || msgSendParent {
			return cobra.MaximumNArgs(1)(cmd, args)
		}
		return cobra.RangeArgs(1, 2)(cmd, args)
	},
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if msgSendChildren || msgSendParent {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return completeSessionNames(cmd, args, toComplete)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if msgSendChildren {
			return msgSendChildrenRun(args)
		}
		if msgSendParent {
			return msgSendParentRun(args)
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
			Quiet:      msgSendQuiet,
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
		out.Print("Sent to %s\n", args[0])
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

var msgTopicsSystem bool

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
			Subscriber:    senderID,
			IncludeSystem: msgTopicsSystem,
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
	msgPubCmd.RegisterFlagCompletionFunc("topic", completeTopicNames)

	msgCmd.AddCommand(msgSendCmd)
	msgSendCmd.Flags().StringVarP(&msgSendFile, "file", "f", "", "read body from file")
	msgSendCmd.Flags().StringVar(&msgSendThreadID, "thread", "", "thread ID to continue")
	msgSendCmd.Flags().StringVar(&msgSendReplyTo, "reply-to", "", "stream for replies")
	msgSendCmd.Flags().BoolVarP(&msgSendQuiet, "quiet", "q", false, "don't type a notification into the session")
	msgSendCmd.Flags().BoolVar(&msgSendChildren, "children", false, "send to all descendant sessions")
	msgSendCmd.Flags().BoolVar(&msgSendParent, "parent", false, "send to parent session")
	msgSendCmd.MarkFlagsMutuallyExclusive("children", "parent")

	msgCmd.AddCommand(msgInboxCmd)
	msgInboxCmd.Flags().BoolVarP(&msgInboxWait, "wait", "w", false, "block until a message arrives")
	msgInboxCmd.Flags().BoolVarP(&msgInboxFollow, "follow", "F", false, "stream new messages continuously")
	msgInboxCmd.Flags().BoolVar(&msgInboxAck, "ack", false, "acknowledge messages after reading")
	msgInboxCmd.Flags().BoolVarP(&msgInboxAll, "all", "a", false, "show all messages, not just unread")
	msgInboxCmd.Flags().StringVar(&msgInboxThreadID, "thread", "", "filter to a specific thread")

	msgCmd.AddCommand(msgSubCmd)
	msgSubCmd.Flags().StringVarP(&msgSubStream, "topic", "t", "", "stream/topic name")
	msgSubCmd.Flags().BoolVarP(&msgSubWait, "wait", "w", false, "block until a message arrives")
	msgSubCmd.Flags().BoolVarP(&msgSubFollow, "follow", "F", false, "stream new messages continuously")
	msgSubCmd.Flags().BoolVar(&msgSubAck, "ack", false, "acknowledge messages after reading")
	msgSubCmd.Flags().BoolVarP(&msgSubAll, "all", "a", false, "show all messages, not just unread")
	msgSubCmd.Flags().StringVar(&msgSubThreadID, "thread", "", "filter to a specific thread")
	msgSubCmd.RegisterFlagCompletionFunc("topic", completeTopicNames)

	msgCmd.AddCommand(msgAckCmd)
	msgAckCmd.Flags().StringVarP(&msgAckStream, "topic", "t", "", "stream/topic name")
	msgAckCmd.RegisterFlagCompletionFunc("topic", completeTopicNames)

	msgCmd.AddCommand(msgTopicsCmd)
	msgTopicsCmd.Flags().BoolVar(&msgTopicsSystem, "system", false, "include _system.* streams")
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

func resolveCurrentSessionInfo(c *client.Client) (*protocol.SessionInfo, error) {
	currentID := os.Getenv("GRAITH_SESSION_ID")
	if currentID == "" {
		return nil, fmt.Errorf("GRAITH_SESSION_ID is not set; run this from inside a graith session")
	}

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, err
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, err
	}
	for i, s := range list.Sessions {
		if s.ID == currentID {
			return &list.Sessions[i], nil
		}
	}
	return nil, fmt.Errorf("current session %q not found in daemon", currentID)
}

func msgSendChildrenRun(args []string) error {
	body, err := resolveBody(args, msgSendFile)
	if err != nil {
		return err
	}

	senderID, senderName := detectSender()

	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	currentID := os.Getenv("GRAITH_SESSION_ID")
	if currentID == "" {
		return fmt.Errorf("--children requires GRAITH_SESSION_ID to be set")
	}

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return err
	}

	descendants := descendantsOf(list.Sessions, currentID)
	if len(descendants) == 0 {
		return fmt.Errorf("no descendant sessions found")
	}

	var sentTo []string
	for _, desc := range descendants {
		c.SendControl("msg_pub", protocol.MsgPubMsg{
			Stream:     "inbox:" + desc.ID,
			Body:       body,
			SenderID:   senderID,
			SenderName: senderName,
			ThreadID:   msgSendThreadID,
			ReplyTo:    msgSendReplyTo,
			Quiet:      msgSendQuiet,
		})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("sending to %s: %s", desc.Name, e.Message)
		}
		sentTo = append(sentTo, desc.Name)
	}

	if jsonOutput {
		return out.JSON(struct {
			SentTo []string `json:"sent_to"`
			Count  int      `json:"count"`
		}{sentTo, len(sentTo)})
	}
	out.Print("Sent to %d descendant sessions\n", len(sentTo))
	return nil
}

func msgSendParentRun(args []string) error {
	body, err := resolveBody(args, msgSendFile)
	if err != nil {
		return err
	}

	senderID, senderName := detectSender()

	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	current, err := resolveCurrentSessionInfo(c)
	if err != nil {
		return err
	}
	if current.ParentID == "" {
		return fmt.Errorf("current session has no parent")
	}

	c.SendControl("msg_pub", protocol.MsgPubMsg{
		Stream:     "inbox:" + current.ParentID,
		Body:       body,
		SenderID:   senderID,
		SenderName: senderName,
		ThreadID:   msgSendThreadID,
		ReplyTo:    msgSendReplyTo,
		Quiet:      msgSendQuiet,
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
	out.Print("Sent to parent session\n")
	return nil
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
