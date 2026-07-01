package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	cfg        *config.Config
	paths      config.Paths
	configFile string
}

func NewServer(cfg *config.Config, paths config.Paths, configFile string) *Server {
	return &Server{cfg: cfg, paths: paths, configFile: configFile}
}

func (s *Server) Run(ctx context.Context) error {
	srv := gomcp.NewServer(
		&gomcp.Implementation{Name: "graith", Version: version.Version},
		nil,
	)

	gomcp.AddTool(srv, &gomcp.Tool{
		Name:        "list_sessions",
		Description: "List all graith sessions with their status, agent type, and metadata.",
	}, s.listSessions)

	gomcp.AddTool(srv, &gomcp.Tool{
		Name:        "session_status",
		Description: "Get detailed status of a specific session by name or ID.",
	}, s.sessionStatus)

	gomcp.AddTool(srv, &gomcp.Tool{
		Name:        "create_session",
		Description: "Create a new graith session with an AI agent in an isolated git worktree.",
	}, s.createSession)

	gomcp.AddTool(srv, &gomcp.Tool{
		Name:        "publish_message",
		Description: "Publish a message to a topic for inter-agent communication.",
	}, s.publishMessage)

	gomcp.AddTool(srv, &gomcp.Tool{
		Name:        "read_inbox",
		Description: "Read messages from this session's inbox. Uses GRAITH_TOKEN to identify the session automatically.",
	}, s.readInbox)

	gomcp.AddTool(srv, &gomcp.Tool{
		Name:        "read_messages",
		Description: "Read messages from a topic. Returns all or only unread messages. Cannot read inbox streams — use read_inbox instead.",
	}, s.readMessages)

	gomcp.AddTool(srv, &gomcp.Tool{
		Name:        "subscribe",
		Description: "Wait for the next message on a topic. Blocks until a message arrives. Cannot subscribe to inbox streams — use read_inbox instead.",
	}, s.subscribe)

	return srv.Run(ctx, &gomcp.StdioTransport{})
}

func (s *Server) connect() (*client.Client, error) {
	return client.ConnectPassive(s.cfg, s.paths, s.configFile)
}

// Tool input/output types

type ListSessionsInput struct{}

type SessionInfoOutput struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	RepoPath       string `json:"repo_path,omitempty"`
	RepoName       string `json:"repo_name,omitempty"`
	WorktreePath   string `json:"worktree_path,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Agent          string `json:"agent"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Status         string `json:"status"`
	AgentStatus    string `json:"agent_status,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
	CreatedAt      string `json:"created_at"`
	LastAttachedAt string `json:"last_attached_at,omitempty"`
	Dirty          bool   `json:"dirty,omitempty"`
	UnpushedCount  int    `json:"unpushed_count,omitempty"`
}

type ListSessionsOutput struct {
	Sessions []SessionInfoOutput `json:"sessions"`
}

type SessionStatusInput struct {
	Session string `json:"session" jsonschema:"Session name or ID"`
}

type CreateSessionInput struct {
	Name            string `json:"name"                       jsonschema:"Human-readable session name"`
	Agent           string `json:"agent,omitempty"            jsonschema:"Agent type (e.g. claude, codex, agy). Defaults to configured default."`
	Repo            string `json:"repo,omitempty"             jsonschema:"Path to the git repository"`
	Base            string `json:"base,omitempty"             jsonschema:"Base branch to create worktree from"`
	Prompt          string `json:"prompt,omitempty"           jsonschema:"Initial prompt to send to the agent"`
	Model           string `json:"model,omitempty"            jsonschema:"Model for the agent to use (expands {model} in agent args)"`
	NoRepo          bool   `json:"no_repo,omitempty"          jsonschema:"Create session without a git worktree"`
	InPlace         bool   `json:"in_place,omitempty"         jsonschema:"Run agent directly in repo without creating a worktree"`
	AllowConcurrent bool   `json:"allow_concurrent,omitempty" jsonschema:"Allow multiple in-place sessions on same repo"`
}

type CreateSessionOutput struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	WorktreePath string `json:"worktree_path,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Agent        string `json:"agent"`
	Status       string `json:"status"`
}

type PublishMessageInput struct {
	Topic    string `json:"topic"               jsonschema:"Topic/stream name to publish to"`
	Body     string `json:"body"                jsonschema:"Message body"`
	Sender   string `json:"sender,omitempty"    jsonschema:"Sender name for attribution"`
	ThreadID string `json:"thread_id,omitempty" jsonschema:"Thread ID for threaded conversations"`
	ReplyTo  string `json:"reply_to,omitempty"  jsonschema:"Topic where replies should be sent"`
}

type PublishMessageOutput struct {
	ID        string `json:"id"`
	Seq       int64  `json:"seq"`
	Stream    string `json:"stream"`
	CreatedAt string `json:"created_at"`
}

type ReadMessagesInput struct {
	Topic      string `json:"topic"                jsonschema:"Topic/stream name to read from"`
	Subscriber string `json:"subscriber,omitempty" jsonschema:"Subscriber ID for tracking read position"`
	All        bool   `json:"all,omitempty"        jsonschema:"Read all messages instead of only unread"`
	ThreadID   string `json:"thread_id,omitempty"  jsonschema:"Filter to a specific thread"`
	Ack        bool   `json:"ack,omitempty"        jsonschema:"Acknowledge messages after reading"`
}

type MessageOutput struct {
	ID         string `json:"id"`
	Seq        int64  `json:"seq"`
	Stream     string `json:"stream"`
	SenderID   string `json:"sender_id"`
	SenderName string `json:"sender_name,omitempty"`
	Body       string `json:"body"`
	ThreadID   string `json:"thread_id,omitempty"`
	ReplyTo    string `json:"reply_to,omitempty"`
	CreatedAt  string `json:"created_at"`
}

type ReadMessagesOutput struct {
	Messages []MessageOutput `json:"messages"`
}

type SubscribeInput struct {
	Topic      string `json:"topic"                jsonschema:"Topic/stream name to subscribe to"`
	Subscriber string `json:"subscriber,omitempty" jsonschema:"Subscriber ID for tracking read position"`
	ThreadID   string `json:"thread_id,omitempty"  jsonschema:"Filter to a specific thread"`
	Ack        bool   `json:"ack,omitempty"        jsonschema:"Acknowledge the message after receiving it"`
}

type SubscribeOutput struct {
	Message MessageOutput `json:"message"`
}

type ReadInboxInput struct {
	All      bool   `json:"all,omitempty"       jsonschema:"Read all messages instead of only unread"`
	ThreadID string `json:"thread_id,omitempty" jsonschema:"Filter to a specific thread"`
	Ack      bool   `json:"ack,omitempty"       jsonschema:"Acknowledge messages after reading"`
}

// Tool handlers

func (s *Server) listSessions(_ context.Context, _ *gomcp.CallToolRequest, _ ListSessionsInput) (*gomcp.CallToolResult, ListSessionsOutput, error) {
	c, err := s.connect()
	if err != nil {
		return nil, ListSessionsOutput{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer c.Close()

	if err := c.SendControl("list", struct{}{}); err != nil {
		return nil, ListSessionsOutput{}, fmt.Errorf("send list: %w", err)
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, ListSessionsOutput{}, fmt.Errorf("read response: %w", err)
	}

	if resp.Type == "error" {
		return nil, ListSessionsOutput{}, decodeError(resp)
	}

	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, ListSessionsOutput{}, fmt.Errorf("decode sessions: %w", err)
	}

	out := ListSessionsOutput{Sessions: make([]SessionInfoOutput, len(list.Sessions))}
	for i, si := range list.Sessions {
		out.Sessions[i] = sessionInfoToOutput(si)
	}

	return nil, out, nil
}

func (s *Server) sessionStatus(_ context.Context, _ *gomcp.CallToolRequest, input SessionStatusInput) (*gomcp.CallToolResult, SessionInfoOutput, error) {
	c, err := s.connect()
	if err != nil {
		return nil, SessionInfoOutput{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer c.Close()

	if err := c.SendControl("list", struct{}{}); err != nil {
		return nil, SessionInfoOutput{}, fmt.Errorf("send list: %w", err)
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, SessionInfoOutput{}, fmt.Errorf("read response: %w", err)
	}

	if resp.Type == "error" {
		return nil, SessionInfoOutput{}, decodeError(resp)
	}

	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, SessionInfoOutput{}, fmt.Errorf("decode sessions: %w", err)
	}

	for _, si := range list.Sessions {
		if si.Name == input.Session || si.ID == input.Session {
			return nil, sessionInfoToOutput(si), nil
		}
	}

	return nil, SessionInfoOutput{}, fmt.Errorf("session %q not found", input.Session)
}

func (s *Server) createSession(_ context.Context, _ *gomcp.CallToolRequest, input CreateSessionInput) (*gomcp.CallToolResult, CreateSessionOutput, error) {
	c, err := s.connect()
	if err != nil {
		return nil, CreateSessionOutput{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer c.Close()

	agent := input.Agent
	if agent == "" {
		agent = s.cfg.DefaultAgent
	}

	if err := c.SendControl("create", protocol.CreateMsg{
		Name:            input.Name,
		Agent:           agent,
		RepoPath:        input.Repo,
		Base:            input.Base,
		Prompt:          input.Prompt,
		Model:           input.Model,
		NoRepo:          input.NoRepo,
		InPlace:         input.InPlace,
		AllowConcurrent: input.AllowConcurrent,
	}); err != nil {
		return nil, CreateSessionOutput{}, fmt.Errorf("send create: %w", err)
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, CreateSessionOutput{}, fmt.Errorf("read response: %w", err)
	}

	if resp.Type == "error" {
		return nil, CreateSessionOutput{}, decodeError(resp)
	}

	var info protocol.SessionInfo
	if err := protocol.DecodePayload(resp, &info); err != nil {
		return nil, CreateSessionOutput{}, fmt.Errorf("decode session: %w", err)
	}

	return nil, CreateSessionOutput{
		ID:           info.ID,
		Name:         info.Name,
		WorktreePath: info.WorktreePath,
		Branch:       info.Branch,
		Agent:        info.Agent,
		Status:       info.Status,
	}, nil
}

func (s *Server) publishMessage(_ context.Context, _ *gomcp.CallToolRequest, input PublishMessageInput) (*gomcp.CallToolResult, PublishMessageOutput, error) {
	c, err := s.connect()
	if err != nil {
		return nil, PublishMessageOutput{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer c.Close()

	senderID := "mcp"

	senderName := input.Sender
	if senderName == "" {
		senderName = "mcp"
	}

	if err := c.SendControl("msg_pub", protocol.MsgPubMsg{
		Stream:     input.Topic,
		Body:       input.Body,
		SenderID:   senderID,
		SenderName: senderName,
		ThreadID:   input.ThreadID,
		ReplyTo:    input.ReplyTo,
	}); err != nil {
		return nil, PublishMessageOutput{}, fmt.Errorf("send publish: %w", err)
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, PublishMessageOutput{}, fmt.Errorf("read response: %w", err)
	}

	if resp.Type == "error" {
		return nil, PublishMessageOutput{}, decodeError(resp)
	}

	var msg PublishMessageOutput
	if err := protocol.DecodePayload(resp, &msg); err != nil {
		return nil, PublishMessageOutput{}, fmt.Errorf("decode published message: %w", err)
	}

	return nil, msg, nil
}

func (s *Server) readMessages(_ context.Context, _ *gomcp.CallToolRequest, input ReadMessagesInput) (*gomcp.CallToolResult, ReadMessagesOutput, error) {
	if strings.HasPrefix(input.Topic, "inbox:") {
		return nil, ReadMessagesOutput{}, fmt.Errorf("cannot read inbox streams via read_messages — use the read_inbox tool instead")
	}

	c, err := s.connect()
	if err != nil {
		return nil, ReadMessagesOutput{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer c.Close()

	subscriber := input.Subscriber
	if subscriber == "" {
		subscriber = "mcp"
	}

	if err := c.SendControl("msg_sub", protocol.MsgSubMsg{
		Stream:     input.Topic,
		Subscriber: subscriber,
		OnlyUnread: !input.All,
		ThreadID:   input.ThreadID,
		Wait:       false,
		Follow:     false,
		Ack:        input.Ack,
	}); err != nil {
		return nil, ReadMessagesOutput{}, fmt.Errorf("send subscribe: %w", err)
	}

	var messages []MessageOutput

	for {
		frame, err := c.ReadFrame()
		if err != nil {
			return nil, ReadMessagesOutput{}, fmt.Errorf("read frame: %w", err)
		}

		if frame.Channel != protocol.ChannelControl {
			continue
		}

		msg, err := protocol.DecodeControl(frame.Payload)
		if err != nil {
			continue
		}

		switch msg.Type {
		case "msg_message":
			var m MessageOutput
			if err := json.Unmarshal(msg.Payload, &m); err == nil {
				messages = append(messages, m)
			}
		case "msg_done":
			if messages == nil {
				messages = []MessageOutput{}
			}

			return nil, ReadMessagesOutput{Messages: messages}, nil
		case "error":
			return nil, ReadMessagesOutput{}, decodeError(msg)
		}
	}
}

func (s *Server) subscribe(ctx context.Context, _ *gomcp.CallToolRequest, input SubscribeInput) (*gomcp.CallToolResult, SubscribeOutput, error) {
	if strings.HasPrefix(input.Topic, "inbox:") {
		return nil, SubscribeOutput{}, fmt.Errorf("cannot subscribe to inbox streams — use the read_inbox tool instead")
	}

	c, err := s.connect()
	if err != nil {
		return nil, SubscribeOutput{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer c.Close()

	subscriber := input.Subscriber
	if subscriber == "" {
		subscriber = "mcp"
	}

	if err := c.SendControl("msg_sub", protocol.MsgSubMsg{
		Stream:     input.Topic,
		Subscriber: subscriber,
		OnlyUnread: true,
		ThreadID:   input.ThreadID,
		Wait:       true,
		Follow:     false,
		Ack:        input.Ack,
	}); err != nil {
		return nil, SubscribeOutput{}, fmt.Errorf("send subscribe: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			_ = c.SendControl("detach", struct{}{})
			return nil, SubscribeOutput{}, ctx.Err()
		default:
		}

		frame, err := c.ReadFrame()
		if err != nil {
			return nil, SubscribeOutput{}, fmt.Errorf("read frame: %w", err)
		}

		if frame.Channel != protocol.ChannelControl {
			continue
		}

		msg, err := protocol.DecodeControl(frame.Payload)
		if err != nil {
			continue
		}

		switch msg.Type {
		case "msg_message":
			var m MessageOutput
			if err := json.Unmarshal(msg.Payload, &m); err == nil {
				return nil, SubscribeOutput{Message: m}, nil
			}
		case "msg_done":
			return nil, SubscribeOutput{}, nil
		case "msg_following":
			// waiting for message, continue reading
		case "error":
			return nil, SubscribeOutput{}, decodeError(msg)
		}
	}
}

func (s *Server) readInbox(_ context.Context, _ *gomcp.CallToolRequest, input ReadInboxInput) (*gomcp.CallToolResult, ReadMessagesOutput, error) {
	c, err := s.connect()
	if err != nil {
		return nil, ReadMessagesOutput{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer c.Close()

	if err := c.SendControl("msg_inbox", protocol.MsgInboxMsg{
		OnlyUnread: !input.All,
		ThreadID:   input.ThreadID,
		Ack:        input.Ack,
	}); err != nil {
		return nil, ReadMessagesOutput{}, fmt.Errorf("send msg_inbox: %w", err)
	}

	var messages []MessageOutput

	for {
		frame, err := c.ReadFrame()
		if err != nil {
			return nil, ReadMessagesOutput{}, fmt.Errorf("read frame: %w", err)
		}

		if frame.Channel != protocol.ChannelControl {
			continue
		}

		msg, err := protocol.DecodeControl(frame.Payload)
		if err != nil {
			continue
		}

		switch msg.Type {
		case "msg_message":
			var m MessageOutput
			if err := json.Unmarshal(msg.Payload, &m); err == nil {
				messages = append(messages, m)
			}
		case "msg_done":
			if messages == nil {
				messages = []MessageOutput{}
			}

			return nil, ReadMessagesOutput{Messages: messages}, nil
		case "error":
			return nil, ReadMessagesOutput{}, decodeError(msg)
		}
	}
}

// Helpers

func decodeError(env protocol.Envelope) error {
	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	return fmt.Errorf("%s", e.Message)
}

func sessionInfoToOutput(si protocol.SessionInfo) SessionInfoOutput {
	return SessionInfoOutput{
		ID:             si.ID,
		Name:           si.Name,
		RepoPath:       si.RepoPath,
		RepoName:       si.RepoName,
		WorktreePath:   si.WorktreePath,
		Branch:         si.Branch,
		Agent:          si.Agent,
		AgentSessionID: si.AgentSessionID,
		Model:          si.Model,
		Status:         si.Status,
		AgentStatus:    si.AgentStatus,
		ExitCode:       si.ExitCode,
		CreatedAt:      si.CreatedAt,
		LastAttachedAt: si.LastAttachedAt,
		Dirty:          si.Dirty,
		UnpushedCount:  si.UnpushedCount,
	}
}
