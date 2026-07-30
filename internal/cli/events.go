package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
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

var eventsFollowCmd = &cobra.Command{
	Use:               "follow <child>",
	Short:             "Follow selected events from a direct child session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE:              runEventFollow,
}

var eventsUnfollowCmd = &cobra.Command{
	Use:               "unfollow <child>",
	Short:             "Stop following selected events from a direct child session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE:              runEventUnfollow,
}

var eventsFollowingCmd = &cobra.Command{
	Use:   "following",
	Short: "List active child event follow rules",
	Args:  cobra.NoArgs,
	RunE:  runEventFollowing,
}

var (
	eventsFollowEvents   []string
	eventsUnfollowEvents []string
)

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

func runEventFollow(_ *cobra.Command, args []string) error {
	if len(eventsFollowEvents) == 0 {
		return errors.New("--events is required (allowed: ci)")
	}

	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	child, err := resolveSessionInfo(c, args[0])
	if err != nil {
		return err
	}

	if err := c.SendControl("event_follow", protocol.EventFollowMsg{
		ChildSessionID: child.ID,
		Events:         append([]string{}, eventsFollowEvents...),
	}); err != nil {
		return err
	}

	info, err := readEventFollowRuleResponse(c, "event_followed")
	if err != nil {
		return err
	}

	return printEventFollowRule(info, "Following")
}

func runEventUnfollow(_ *cobra.Command, args []string) error {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	child, err := resolveSessionInfo(c, args[0])
	if err != nil {
		return err
	}

	if err := c.SendControl("event_unfollow", protocol.EventUnfollowMsg{
		ChildSessionID: child.ID,
		Events:         append([]string{}, eventsUnfollowEvents...),
	}); err != nil {
		return err
	}

	info, err := readEventFollowRuleResponse(c, "event_unfollowed")
	if err != nil {
		return err
	}

	return printEventFollowRule(info, "Unfollowed")
}

func runEventFollowing(_ *cobra.Command, _ []string) error {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.SendControl("event_following", protocol.EventFollowingMsg{}); err != nil {
		return err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		return fmt.Errorf("%s", errorMessage(resp))
	}

	if resp.Type != "event_following" {
		return fmt.Errorf("unexpected event_following response: %s", resp.Type)
	}

	var rules protocol.EventFollowingResponseMsg
	if err := protocol.DecodePayload(resp, &rules); err != nil {
		return fmt.Errorf("decode event_following response: %w", err)
	}

	if jsonOutput {
		return out.JSON(rules)
	}

	if len(rules.Rules) == 0 {
		out.Printf("No event follow rules\n")
		return nil
	}

	for _, rule := range rules.Rules {
		printEventFollowRuleLine(rule)
	}

	return nil
}

func readEventFollowRuleResponse(c controlConn, wantType string) (protocol.EventFollowRuleInfo, error) {
	resp, err := c.ReadControlResponse()
	if err != nil {
		return protocol.EventFollowRuleInfo{}, err
	}

	if resp.Type == "error" {
		return protocol.EventFollowRuleInfo{}, fmt.Errorf("%s", errorMessage(resp))
	}

	if resp.Type != wantType {
		return protocol.EventFollowRuleInfo{}, fmt.Errorf("unexpected %s response: %s", wantType, resp.Type)
	}

	var info protocol.EventFollowRuleInfo
	if err := protocol.DecodePayload(resp, &info); err != nil {
		return protocol.EventFollowRuleInfo{}, fmt.Errorf("decode %s response: %w", wantType, err)
	}

	return info, nil
}

func printEventFollowRule(info protocol.EventFollowRuleInfo, verb string) error {
	if jsonOutput {
		return out.JSON(info)
	}

	out.Printf("%s ", verb)
	printEventFollowRuleLine(info)

	return nil
}

func printEventFollowRuleLine(info protocol.EventFollowRuleInfo) {
	child := info.ChildSession
	if child == "" {
		child = info.ChildSessionID
	}

	parent := info.ParentSession
	if parent == "" {
		parent = info.ParentSessionID
	}

	events := "none"
	if len(info.Events) > 0 {
		events = strings.Join(info.Events, ",")
	}

	out.Printf("%s from %s -> %s\n", events, child, parent)
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
	case "session_event":
		if ev.Forwarded && ev.EventClass == "ci" {
			source := ev.SourceSession
			if source == "" {
				source = ev.SourceSessionID
			}

			_, _ = fmt.Fprintf(w, "[%s] forwarded ci from %s: %s on PR #%d\n", ev.At, source, ev.CIState, ev.PRNumber)
		} else {
			_, _ = fmt.Fprintf(w, "[%s] session event: %s\n", ev.At, ev.EventClass)
		}
	default:
		_, _ = fmt.Fprintf(w, "[%s] %s\n", ev.At, ev.Type)
	}

	return nil
}

// registerEventsCmd registers this command on rootCmd. Called from
// registerCommands.
func registerEventsCmd() {
	eventsFollowCmd.Flags().StringSliceVar(&eventsFollowEvents, "events", nil, "event classes to follow (allowed: ci)")
	eventsUnfollowCmd.Flags().StringSliceVar(&eventsUnfollowEvents, "events", nil, "event classes to stop following (default: all)")
	eventsCmd.AddCommand(eventsFollowCmd, eventsUnfollowCmd, eventsFollowingCmd)
	rootCmd.AddCommand(eventsCmd)
}
