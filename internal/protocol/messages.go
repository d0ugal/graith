package protocol

import (
	"encoding/json"
	"fmt"
)

type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func EncodeControl(msgType string, payload any) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return json.Marshal(Envelope{Type: msgType, Payload: p})
}

func DecodeControl(raw []byte) (Envelope, error) {
	var m Envelope
	if err := json.Unmarshal(raw, &m); err != nil {
		return Envelope{}, fmt.Errorf("decode control: %w", err)
	}
	return m, nil
}

func DecodePayload(m Envelope, target any) error {
	return json.Unmarshal(m.Payload, target)
}

// Client -> Daemon
type HandshakeMsg struct {
	Version      string    `json:"version"`
	ClientID     string    `json:"client_id"`
	TerminalSize [2]uint16 `json:"terminal_size"`
	Cwd          string    `json:"cwd"`
}

type CreateMsg struct {
	Name     string `json:"name"`
	Agent    string `json:"agent"`
	RepoPath string `json:"repo_path"`
	Base     string `json:"base,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	NoRepo   bool   `json:"no_repo,omitempty"`
}

type AttachMsg struct {
	SessionID string `json:"session_id"`
}

type DeleteMsg struct {
	SessionID string `json:"session_id"`
}

type RenameMsg struct {
	SessionID string `json:"session_id"`
	NewName   string `json:"new_name"`
}

type StopMsg struct {
	SessionID string `json:"session_id"`
}

type ResumeMsg struct {
	SessionID string `json:"session_id"`
}

type ResizeMsg struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type LogsMsg struct {
	SessionID string `json:"session_id"`
	Lines     int    `json:"lines"`
	Follow    bool   `json:"follow"`
}

type ScrollbackMsg struct {
	SessionID string `json:"session_id"`
	Lines     int    `json:"lines"`
}

type SearchMsg struct {
	SessionID string `json:"session_id"`
	Query     string `json:"query"`
	Direction string `json:"direction"`
}

type ConfirmResponseMsg struct {
	ConfirmID string `json:"confirm_id"`
	Confirmed bool   `json:"confirmed"`
}

type MsgPubMsg struct {
	Stream     string `json:"stream"`
	Body       string `json:"body"`
	SenderID   string `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	ReplyTo    string `json:"reply_to,omitempty"`
}

type MsgSubMsg struct {
	Stream     string `json:"stream"`
	Subscriber string `json:"subscriber"`
	OnlyUnread bool   `json:"only_unread"`
	ThreadID   string `json:"thread_id,omitempty"`
	Wait       bool   `json:"wait"`
	Follow     bool   `json:"follow"`
	Ack        bool   `json:"ack"`
}

type MsgAckMsg struct {
	Stream     string `json:"stream"`
	Subscriber string `json:"subscriber"`
}

type MsgTopicsMsg struct {
	Subscriber string `json:"subscriber"`
}

// Daemon -> Client
type HandshakeOkMsg struct {
	Version       string `json:"version"`
	DaemonVersion string `json:"daemon_version"`
}

type HandshakeErrMsg struct {
	Reason string `json:"reason"`
}

type SessionListMsg struct {
	Sessions []SessionInfo `json:"sessions"`
}

type SessionInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	RepoPath       string `json:"repo_path"`
	RepoName       string `json:"repo_name"`
	WorktreePath   string `json:"worktree_path"`
	Branch         string `json:"branch"`
	Agent          string `json:"agent"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	Status         string `json:"status"`
	AgentStatus    string `json:"agent_status,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
	CreatedAt      string `json:"created_at"`
	Dirty          bool   `json:"dirty,omitempty"`
	UnpushedCount  int    `json:"unpushed_count,omitempty"`
}

type DetachedMsg struct {
	Reason string `json:"reason"`
}

type ErrorMsg struct {
	Message string `json:"message"`
}

type ConfirmMsg struct {
	ConfirmID string `json:"confirm_id"`
	Prompt    string `json:"prompt"`
}

type SessionUpdateMsg struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}
