package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

const Version = "1.0"

func VersionCompatible(v string) bool {
	ourMajor, _, ok := strings.Cut(Version, ".")
	if !ok {
		return false
	}
	theirMajor, _, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	return ourMajor == theirMajor
}

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
	if len(m.Payload) == 0 || string(m.Payload) == "null" {
		return fmt.Errorf("decode payload: missing or null payload")
	}
	return json.Unmarshal(m.Payload, target)
}

// Client -> Daemon
type HandshakeMsg struct {
	Version      string    `json:"version"`
	ClientID     string    `json:"client_id"`
	TerminalSize [2]uint16 `json:"terminal_size"`
	Cwd          string    `json:"cwd"`
	Profile      string    `json:"profile,omitempty"`
}

type CreateMsg struct {
	Name            string `json:"name"`
	ParentID        string `json:"parent_id,omitempty"`
	Agent           string `json:"agent"`
	RepoPath        string `json:"repo_path"`
	Base            string `json:"base,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	Model           string `json:"model,omitempty"`
	NoRepo          bool   `json:"no_repo,omitempty"`
	ShareWorktree   string `json:"share_worktree,omitempty"`
	AgentHooks      bool   `json:"agent_hooks,omitempty"`
	InPlace         bool   `json:"in_place,omitempty"`
	AllowConcurrent bool   `json:"allow_concurrent,omitempty"`
}

type ForkMsg struct {
	Name            string `json:"name"`
	SourceSessionID string `json:"source_session_id"`
}

type AttachMsg struct {
	SessionID string `json:"session_id"`
}

type DeleteMsg struct {
	SessionID   string `json:"session_id"`
	Children    bool   `json:"children,omitempty"`
	ExcludeRoot bool   `json:"exclude_root,omitempty"`
}

type RenameMsg struct {
	SessionID string `json:"session_id"`
	NewName   string `json:"new_name"`
}

type StarMsg struct {
	SessionID string `json:"session_id"`
}

type UnstarMsg struct {
	SessionID string `json:"session_id"`
}

type SetStatusMsg struct {
	SessionID  string `json:"session_id"`
	Text       string `json:"text"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
	Clear      bool   `json:"clear,omitempty"`
}

type StatusSetMsg struct {
	SessionID string `json:"session_id"`
}

type StopMsg struct {
	SessionID   string `json:"session_id"`
	Children    bool   `json:"children,omitempty"`
	ExcludeRoot bool   `json:"exclude_root,omitempty"`
}

type TypeMsg struct {
	SessionID string `json:"session_id"`
	Input     string `json:"input"`
	NoNewline bool   `json:"no_newline,omitempty"`
}

type ResumeMsg struct {
	SessionID string `json:"session_id"`
}

type RestartMsg struct {
	SessionID string `json:"session_id"`
}

type UpgradeMsg struct {
	ExecPath      string `json:"exec_path,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
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
	Subscriber    string `json:"subscriber"`
	IncludeSystem bool   `json:"include_system,omitempty"`
}

// Client -> Daemon (hook reporting)
type StatusReportMsg struct {
	SessionID string         `json:"session_id"`
	Event     string         `json:"event"`
	Status    string         `json:"status,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	Model     string         `json:"model,omitempty"`
	Usage     *UsageReport   `json:"usage,omitempty"`
	Context   *ContextReport `json:"context,omitempty"`
}

type UsageReport struct {
	InputTokens  *int64   `json:"input_tokens,omitempty"`
	OutputTokens *int64   `json:"output_tokens,omitempty"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
}

type ContextReport struct {
	UsedTokens *int64   `json:"used_tokens,omitempty"`
	MaxTokens  *int64   `json:"max_tokens,omitempty"`
	Percent    *float64 `json:"percent,omitempty"`
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
	ID              string             `json:"id"`
	ParentID        string             `json:"parent_id,omitempty"`
	Name            string             `json:"name"`
	RepoPath        string             `json:"repo_path"`
	RepoName        string             `json:"repo_name"`
	WorktreePath    string             `json:"worktree_path"`
	Branch          string             `json:"branch"`
	BaseBranch      string             `json:"base_branch"`
	Agent           string             `json:"agent"`
	AgentSessionID  string             `json:"agent_session_id,omitempty"`
	Status          string             `json:"status"`
	AgentStatus     string             `json:"agent_status,omitempty"`
	ExitCode        *int               `json:"exit_code,omitempty"`
	CreatedAt       string             `json:"created_at"`
	LastAttachedAt  string             `json:"last_attached_at,omitempty"`
	StatusChangedAt string             `json:"status_changed_at,omitempty"`
	Dirty           bool               `json:"dirty,omitempty"`
	UnpushedCount   int                `json:"unpushed_count,omitempty"`
	Sandboxed       bool               `json:"sandboxed,omitempty"`
	SharedWorktree  bool               `json:"shared_worktree,omitempty"`
	InPlace         bool               `json:"in_place,omitempty"`
	Model           string             `json:"model,omitempty"`
	ToolName        string             `json:"tool_name,omitempty"`
	CostUSD         *float64           `json:"cost_usd,omitempty"`
	ContextPercent  *float64           `json:"context_percent,omitempty"`
	Includes        []IncludedRepoInfo `json:"includes,omitempty"`
	ConfigStale     bool               `json:"config_stale,omitempty"`
	Starred         bool               `json:"starred,omitempty"`
	SystemKind      string             `json:"system_kind,omitempty"`
	SummaryText     string             `json:"summary_text,omitempty"`
	SummaryFaded    bool               `json:"summary_faded,omitempty"`
	LastOutputAt    string             `json:"last_output_at,omitempty"`
}

type IncludedRepoInfo struct {
	RepoName     string `json:"repo_name"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	BaseBranch   string `json:"base_branch"`
	Dirty        bool   `json:"dirty,omitempty"`
	Unpushed     int    `json:"unpushed,omitempty"`
}

type DetachedMsg struct {
	Reason string `json:"reason"`
}

type ErrorMsg struct {
	Message string `json:"message"`
}

type ScreenPreviewMsg struct {
	SessionID string `json:"session_id"`
}

type ScreenPreviewResponseMsg struct {
	SessionID string `json:"session_id"`
	Preview   string `json:"preview"`
}

// Approval protocol messages

// ApprovalRequestMsg is sent by the hook CLI (gr approve-request) to the daemon.
// The handler blocks until a decision is made.
type ApprovalRequestMsg struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	ToolName  string `json:"tool_name"`
	ToolInput string `json:"tool_input,omitempty"`
}

type ApprovalInfo struct {
	RequestID   string `json:"request_id"`
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
	ToolName    string `json:"tool_name"`
	ToolInput   string `json:"tool_input,omitempty"`
	Agent       string `json:"agent"`
	RepoName    string `json:"repo_name"`
	RequestedAt string `json:"requested_at"`
}

type ApprovalNotificationMsg struct {
	Pending []ApprovalInfo `json:"pending"`
}

type ApprovalRespondMsg struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason,omitempty"`
}

type ApprovalDecisionMsg struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

type ScreenSnapshotMsg struct {
	SessionID string `json:"session_id"`
}

type ScreenSnapshotResponseMsg struct {
	SessionID     string `json:"session_id"`
	Frame         string `json:"frame"`
	CursorX       int    `json:"cursor_x"`
	CursorY       int    `json:"cursor_y"`
	CursorVisible bool   `json:"cursor_visible"`
	Cols          int    `json:"cols"`
	Rows          int    `json:"rows"`
}

// MCP proxy messages

type MCPConnectMsg struct {
	Server    string `json:"server"`
	SessionID string `json:"session_id"`
}

type MCPConnectOkMsg struct {
	Server  string `json:"server"`
	Channel byte   `json:"channel"`
}

type StatusRequestMsg struct {
	SessionID string `json:"session_id"`
}

// Diagnostics types (daemon -> client, in response to "diagnostics" message)

type DiagnosticsMsg struct {
	DaemonPID    int                  `json:"daemon_pid"`
	DaemonUptime string               `json:"daemon_uptime"`
	Fleet        FleetSummary         `json:"fleet"`
	Sessions     []SessionDiagnostic  `json:"sessions"`
	Scrollback   ScrollbackDiagnostic `json:"scrollback"`
	Messages     MessagesDiagnostic   `json:"messages"`
}

type SessionDiagnostic struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	AgentStatus     string `json:"agent_status,omitempty"`
	PID             int    `json:"pid,omitempty"`
	PIDAlive        bool   `json:"pid_alive"`
	WorktreePath    string `json:"worktree_path,omitempty"`
	WorktreeExists  bool   `json:"worktree_exists"`
	ConfigStale     bool   `json:"config_stale"`
	HookStale       bool   `json:"hook_stale"`
	ScrollbackBytes int64  `json:"scrollback_bytes"`
	ScrollbackMax   int64  `json:"scrollback_max"`
	Saturated       bool   `json:"saturated"`
}

type ScrollbackDiagnostic struct {
	TotalFiles     int   `json:"total_files"`
	TotalBytes     int64 `json:"total_bytes"`
	SaturatedCount int   `json:"saturated_count"`
}

type MessagesDiagnostic struct {
	TotalStreams  int   `json:"total_streams"`
	TotalMessages int64 `json:"total_messages"`
}

type FleetSummary struct {
	Total    int `json:"total"`
	Active   int `json:"active"`
	Approval int `json:"approval"`
	Ready    int `json:"ready"`
	Errored  int `json:"errored"`
	Stopped  int `json:"stopped"`
}

type StatusResponseMsg struct {
	Session     SessionInfo  `json:"session"`
	UnreadCount int          `json:"unread_count"`
	Fleet       FleetSummary `json:"fleet"`
}
