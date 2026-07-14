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
	Token   string          `json:"token,omitempty"`
}

func EncodeControl(msgType string, payload any) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	return json.Marshal(Envelope{Type: msgType, Payload: p})
}

func EncodeControlWithToken(msgType string, payload any, token string) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	return json.Marshal(Envelope{Type: msgType, Payload: p, Token: token})
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

// HandshakeMsg is sent by the client to the daemon to open a connection.
type HandshakeMsg struct {
	Version      string    `json:"version"`
	ClientID     string    `json:"client_id"`
	TerminalSize [2]uint16 `json:"terminal_size"`
	Cwd          string    `json:"cwd"`
	Profile      string    `json:"profile,omitempty"`
}

type CreateMsg struct {
	Name                string `json:"name"`
	ParentID            string `json:"parent_id,omitempty"`
	Agent               string `json:"agent"`
	RepoPath            string `json:"repo_path"`
	Base                string `json:"base,omitempty"`
	Prompt              string `json:"prompt,omitempty"`
	Model               string `json:"model,omitempty"`
	NoRepo              bool   `json:"no_repo,omitempty"`
	Mirror              string `json:"mirror,omitempty"`
	AgentHooks          bool   `json:"agent_hooks,omitempty"`
	InPlace             bool   `json:"in_place,omitempty"`
	AllowConcurrent     bool   `json:"allow_concurrent,omitempty"`
	SkipModelValidation bool   `json:"skip_model_validation,omitempty"`
	Yolo                bool   `json:"yolo,omitempty"`
	Headless            bool   `json:"headless,omitempty"`
	// NoFetch skips the `git fetch origin` that normally runs before the
	// worktree is created (issue #1012). Lets sessions be created from local
	// repo state when SSH auth is unavailable (Secretive/biometric, offline).
	NoFetch bool `json:"no_fetch,omitempty"`
}

type ForkMsg struct {
	Name            string `json:"name"`
	SourceSessionID string `json:"source_session_id"`
}

type MigrateMsg struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Model     string `json:"model,omitempty"`
}

type AttachMsg struct {
	SessionID string `json:"session_id"`
}

type DeleteMsg struct {
	SessionID   string `json:"session_id"`
	Children    bool   `json:"children,omitempty"`
	ExcludeRoot bool   `json:"exclude_root,omitempty"`
	// Purge requests an immediate hard delete (worktree, branch, and state
	// removed), bypassing the soft-delete retention window. Set only by the
	// `gr purge` verb; `gr delete` never sets it. When false and soft delete is
	// enabled the session is marked deleted and kept for recovery; when false
	// and retention is 0 the daemon rejects the request.
	Purge bool `json:"purge,omitempty"`
}

// ListMsg is the (optional) payload for a "list" control message. Deleted
// requests the soft-deleted sessions instead of the live ones; the default
// (false) returns only non-deleted sessions.
type ListMsg struct {
	Deleted bool `json:"deleted,omitempty"`
}

// RestoreMsg un-deletes a soft-deleted session, returning it to stopped state.
// Children restores the whole soft-deleted subtree rooted at SessionID.
type RestoreMsg struct {
	SessionID string `json:"session_id"`
	Children  bool   `json:"children,omitempty"`
}

// DeleteResultMsg is the daemon's response to a delete. Unlike the bare
// {session_id} the shared lifecycle handler emits, it carries whether the
// session was soft-deleted or hard-purged and, for a soft delete, the computed
// expiry — so the CLI can render "Recoverable until …" vs "Deleted".
//
// For a single delete the top-level fields describe that session. For a
// --children delete the top-level SessionID/Soft describe the *request* (the
// requested root and whether the operation was soft or hard), and Affected is
// the authoritative FLAT list of per-session outcomes (one entry per session
// actually acted on — including the root unless --exclude-root was set), each
// with its own Name/DeletedAt/ExpiresAt. It is not a nested tree.
type DeleteResultMsg struct {
	SessionID string            `json:"session_id"`
	Name      string            `json:"name,omitempty"`
	Soft      bool              `json:"soft"`
	DeletedAt string            `json:"deleted_at,omitempty"` // RFC3339, set when Soft
	ExpiresAt string            `json:"expires_at,omitempty"` // RFC3339, frozen deadline, when Soft
	Affected  []DeleteResultMsg `json:"affected,omitempty"`
}

// RestoreResultMsg is the daemon's response to a restore. Sessions holds the
// restored session(s) (one for a bare restore, the whole subtree for
// --children). DeletedDescendants counts descendants still soft-deleted after a
// bare restore, so the CLI can warn that a subtree remains hidden.
type RestoreResultMsg struct {
	Sessions           []SessionInfo `json:"sessions"`
	DeletedDescendants int           `json:"deleted_descendants,omitempty"`
}

// GCMsg requests orphan garbage collection. When Force is false (the default)
// the daemon returns a dry-run listing without deleting anything.
type GCMsg struct {
	Force bool `json:"force,omitempty"`
}

// GCOrphanInfo describes one orphaned directory in a GCResultMsg.
type GCOrphanInfo struct {
	Type          string `json:"type"`
	Path          string `json:"path"`
	ID            string `json:"id"`
	IsGitWorktree bool   `json:"is_git_worktree,omitempty"`
	HasDirtyFiles bool   `json:"has_dirty_files,omitempty"`
	// DirtyUndetermined is set when HasDirtyFiles was forced true because the
	// git state could not be read (not because changes were confirmed), so the
	// CLI can distinguish "has WIP" from "couldn't check".
	DirtyUndetermined bool   `json:"dirty_undetermined,omitempty"`
	Removed           bool   `json:"removed,omitempty"`
	Skipped           bool   `json:"skipped,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

// GCResultMsg is the daemon's reply to a GCMsg.
type GCResultMsg struct {
	DryRun  bool           `json:"dry_run"`
	Orphans []GCOrphanInfo `json:"orphans"`
}

type RenameMsg struct {
	SessionID string `json:"session_id"`
	NewName   string `json:"new_name"`
}

type UpdateMsg struct {
	SessionID string  `json:"session_id"`
	Name      *string `json:"name,omitempty"`
	ParentID  *string `json:"parent_id,omitempty"`
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

type InterruptMsg struct {
	SessionID string `json:"session_id"`
}

type ResumeMsg struct {
	SessionID string `json:"session_id"`
}

type RestartMsg struct {
	SessionID   string `json:"session_id"`
	Children    bool   `json:"children,omitempty"`
	ExcludeRoot bool   `json:"exclude_root,omitempty"`
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

// WaitMsg asks the daemon to block until a session satisfies a condition.
// Mode selects the condition:
//   - "contains": Pattern (a regexp) matches a line of the session's output
//   - "status":   the session's lifecycle status equals Status
//   - "idle":     the session's agent becomes idle (ready/unknown)
//
// TimeoutMs, when > 0, bounds the wait; the daemon replies with a timeout
// result if the condition is not met in time.
type WaitMsg struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`
	Pattern   string `json:"pattern,omitempty"`
	Status    string `json:"status,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

// WaitMatchedMsg is sent by the daemon when a wait condition is satisfied.
// MatchedLine carries the matching output line (contains mode); Status carries
// the observed status (status/idle modes).
type WaitMatchedMsg struct {
	MatchedLine string `json:"matched_line,omitempty"`
	Status      string `json:"status,omitempty"`
}

type MsgPubMsg struct {
	Stream     string `json:"stream"`
	Body       string `json:"body"`
	SenderID   string `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	ReplyTo    string `json:"reply_to,omitempty"`
	Quiet      bool   `json:"quiet,omitempty"`
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

type MsgInboxMsg struct {
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

// MsgConversationMsg requests the full direct-message conversation (both
// directions) for SessionID. Authorisation uses the self-or-descendant rule, so
// a human CLI, the session itself, an ancestor, or the orchestrator may read it.
type MsgConversationMsg struct {
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit,omitempty"`
}

// ConversationMessage is a single message in a conversation, mirroring the
// daemon's stored message shape on the wire.
type ConversationMessage struct {
	ID         string `json:"id"`
	Seq        int64  `json:"seq"`
	Stream     string `json:"stream"`
	SenderID   string `json:"sender_id"`
	SenderName string `json:"sender_name,omitempty"`
	Body       string `json:"body"`
	ThreadID   string `json:"thread_id,omitempty"`
	ReplyTo    string `json:"reply_to,omitempty"`
	CreatedAt  string `json:"created_at"`
	// System marks an automated daemon-authored notification rather than a
	// session/human message. See issue #887.
	System bool `json:"system,omitempty"`
}

// MsgConversationListMsg is the daemon's response to msg_conversation.
type MsgConversationListMsg struct {
	Messages []ConversationMessage `json:"messages"`
}

// --- PR-comment jail (issue #1082) ---

// JailedCommentInfo is the wire form of a quarantined PR comment. It mirrors the
// daemon's JailedComment. The body is included for `show`/`list`; a client may
// choose to elide it in list output.
type JailedCommentInfo struct {
	ID            string `json:"id"`
	CommentID     int64  `json:"comment_id"`
	Surface       string `json:"surface"`
	PRNumber      int    `json:"pr_number"`
	RepoSlug      string `json:"repo_slug,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Author        string `json:"author"`
	Association   string `json:"association,omitempty"`
	IsBot         bool   `json:"is_bot,omitempty"`
	Path          string `json:"path,omitempty"`
	Line          int    `json:"line,omitempty"`
	Body          string `json:"body"`
	TargetSession string `json:"target_session"`
	TargetName    string `json:"target_name,omitempty"`
	JailedAt      string `json:"jailed_at"`
	ReleasedAt    string `json:"released_at,omitempty"`
}

// MsgJailListMsg requests the list of jailed PR comments (read-only). When
// IncludeReleased is true, already-released entries are included too.
type MsgJailListMsg struct {
	IncludeReleased bool `json:"include_released,omitempty"`
}

// MsgJailListResponse is the daemon's reply to msg_jail_list.
type MsgJailListResponse struct {
	Jailed []JailedCommentInfo `json:"jailed"`
}

// MsgJailShowMsg requests a single jailed comment by ID (read-only).
type MsgJailShowMsg struct {
	ID string `json:"id"`
}

// MsgJailShowResponse is the daemon's reply to msg_jail_show.
type MsgJailShowResponse struct {
	Jailed JailedCommentInfo `json:"jailed"`
}

// MsgJailReleaseMsg releases a jailed comment (or all from an author). Gated to
// the human or the orchestrator — a plain agent session is rejected. Exactly one
// of ID, or (All && Author), is expected.
type MsgJailReleaseMsg struct {
	ID     string `json:"id,omitempty"`
	All    bool   `json:"all,omitempty"`
	Author string `json:"author,omitempty"`
}

// MsgJailReleaseResponse is the daemon's reply to msg_jail_release. Released
// carries the entries that were delivered to their target sessions.
type MsgJailReleaseResponse struct {
	Released []JailedCommentInfo `json:"released"`
}

// NotifyMsg is a client request (`gr notify`) to send a proactive push
// notification to the human via the configured [notifications] backend.
type NotifyMsg struct {
	Message  string `json:"message"`
	Title    string `json:"title,omitempty"`
	Priority string `json:"priority,omitempty"` // low | normal | high (default normal)
}

// NotifyResponse is the daemon's reply to a notify request. Delivered is false
// when the notification was intentionally suppressed (disabled, quiet hours,
// rate-limited, coalesced); Reason carries a human-readable explanation.
type NotifyResponse struct {
	Delivered bool   `json:"delivered"`
	Reason    string `json:"reason,omitempty"`
}

// StatusReportMsg is sent by the client to the daemon to report hook events.
type StatusReportMsg struct {
	SessionID string `json:"session_id"`
	Event     string `json:"event"`
	Status    string `json:"status,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	// NotificationType carries the Claude Notification subtype (e.g.
	// idle_prompt, permission_prompt) raw from the hook payload. The CLI
	// forwards it verbatim (empty when stdin didn't parse) and the daemon
	// decides what it means — see HandleHookReport.
	NotificationType string `json:"notification_type,omitempty"`
	// Trigger carries the compaction trigger ("manual" | "auto") for
	// Pre/PostCompact events.
	Trigger string `json:"trigger,omitempty"`
	// AgentID / AgentType identify a Claude sub-agent for SubagentStart /
	// SubagentStop events.
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	// Reason carries Claude's SessionEnd reason (clear/resume/logout/
	// prompt_input_exit/other). It is logical-session metadata, mapped to a
	// StopReason only for process-ending reasons — see mapSessionEndReason.
	Reason string `json:"reason,omitempty"`
	// LastMessage carries the (already CLI-truncated) last_assistant_message
	// from a Stop event. It is captured runtime-only and never persisted.
	LastMessage string `json:"last_message,omitempty"`
}

// HandshakeOkMsg is the daemon's response to a successful handshake.
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
	ExitSignal      string             `json:"exit_signal,omitempty"`
	CreatedAt       string             `json:"created_at"`
	LastAttachedAt  string             `json:"last_attached_at,omitempty"`
	StatusChangedAt string             `json:"status_changed_at,omitempty"`
	Dirty           bool               `json:"dirty,omitempty"`
	UnpushedCount   int                `json:"unpushed_count,omitempty"`
	Sandboxed       bool               `json:"sandboxed,omitempty"`
	Mirror          bool               `json:"mirror,omitempty"`
	InPlace         bool               `json:"in_place,omitempty"`
	Yolo            bool               `json:"yolo,omitempty"`
	Model           string             `json:"model,omitempty"`
	ToolName        string             `json:"tool_name,omitempty"`
	Includes        []IncludedRepoInfo `json:"includes,omitempty"`
	ConfigStale     bool               `json:"config_stale,omitempty"`
	Starred         bool               `json:"starred,omitempty"`
	SystemKind      string             `json:"system_kind,omitempty"`
	ScenarioID      string             `json:"scenario_id,omitempty"`
	ScenarioName    string             `json:"scenario_name,omitempty"`
	SummaryText     string             `json:"summary_text,omitempty"`
	SummaryFaded    bool               `json:"summary_faded,omitempty"`
	LastOutputAt    string             `json:"last_output_at,omitempty"`
	MigratedFrom    string             `json:"migrated_from,omitempty"`
	PullRequest     *PRInfo            `json:"pull_request,omitempty"`
	CI              *CIInfo            `json:"ci,omitempty"`
	// Tokens is the aggregated token usage for the session's current agent, or
	// nil when unknown (never parsed, unsupported agent, or unreadable). Absent
	// via omitempty so older clients and pre-token daemons project nothing.
	Tokens *TokenInfo `json:"tokens,omitempty"`
	// DeletedAt is set (RFC3339) when the session has been soft-deleted.
	DeletedAt string `json:"deleted_at,omitempty"`
	// DeleteExpiresAt is the RFC3339 time at which a soft-deleted session will
	// be purged (DeletedAt + retention). Empty when the session is not deleted.
	DeleteExpiresAt string `json:"delete_expires_at,omitempty"`
	// ContextPressure is true when the session is compacting or has signalled
	// context pressure (Claude's PreCompact, cleared by PostCompact). Runtime
	// only — a daemon restart forgets it until the next compaction signal.
	ContextPressure bool `json:"context_pressure,omitempty"`
	// SubAgentCount is the number of Claude sub-agents currently running under
	// this session (SubagentStart/SubagentStop). Runtime only.
	SubAgentCount int `json:"sub_agent_count,omitempty"`
}

// PRInfo is the linked GitHub pull request for a session's branch.
type PRInfo struct {
	Number         int    `json:"number"`
	State          string `json:"state"` // open | draft | merged | closed
	URL            string `json:"url,omitempty"`
	ReviewDecision string `json:"review_decision,omitempty"` // approved | changes_requested | review_required
	Conflicting    bool   `json:"conflicting,omitempty"`     // PR has merge conflicts with base
}

// CIInfo is the aggregate CI status for a session's linked PR.
type CIInfo struct {
	State         string   `json:"state"` // passing | failing | pending
	FailingChecks []string `json:"failing_checks,omitempty"`
}

// TokenInfo is the aggregated token usage for a session's current agent. The
// four categories plus Unclassified are mutually exclusive, so Total is a real
// total. A present TokenInfo means at least one usage record was observed.
type TokenInfo struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheCreation int64 `json:"cache_creation,omitempty"`
	CacheRead     int64 `json:"cache_read,omitempty"`
	Unclassified  int64 `json:"unclassified,omitempty"`
	Total         int64 `json:"total"`
	// Degraded marks an approximate count (parse conflicts / un-dedupable records).
	Degraded bool `json:"degraded,omitempty"`
	// CountedAt is the RFC3339 time of the last successful observation.
	CountedAt string `json:"counted_at,omitempty"`
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
//
// ToolInput carries the FULL, untruncated tool input JSON — approvals backends
// may need to evaluate the whole command, so truncation happens only at the
// display layer (ApprovalInfo). HookPayload is the raw agent hook payload,
// forwarded verbatim for backends (e.g. localmost) that speak the agent's
// native protocol; it may be empty.
type ApprovalRequestMsg struct {
	RequestID   string `json:"request_id"`
	SessionID   string `json:"session_id"`
	ToolName    string `json:"tool_name"`
	ToolInput   string `json:"tool_input,omitempty"`
	HookPayload string `json:"hook_payload,omitempty"`
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

// MCP management messages (gr mcp list/restart/logs)

// MCPListMsg requests the status of all configured MCP servers.
type MCPListMsg struct{}

// MCPConnectionInfo describes a single running MCP server process (one per
// active proxy connection).
type MCPConnectionInfo struct {
	ProxyID   string `json:"proxy_id"`
	PID       int    `json:"pid"`
	Running   bool   `json:"running"`
	Uptime    string `json:"uptime"`
	UptimeSec int    `json:"uptime_seconds"`
}

// MCPServerStatus describes a configured MCP server and its live processes.
type MCPServerStatus struct {
	Name         string              `json:"name"`
	Sandboxed    bool                `json:"sandboxed"`
	AutoInjected bool                `json:"auto_injected,omitempty"`
	Connections  []MCPConnectionInfo `json:"connections,omitempty"`
}

// MCPListResponse carries the configured MCP servers and their status.
type MCPListResponse struct {
	Servers []MCPServerStatus `json:"servers"`
}

// MCPRestartMsg requests that all running processes for a named MCP server be
// stopped so proxies reconnect and get fresh processes.
type MCPRestartMsg struct {
	Name string `json:"name"`
}

// MCPRestartResponse reports how many processes were stopped by a restart.
type MCPRestartResponse struct {
	Name    string `json:"name"`
	Stopped int    `json:"stopped"`
}

// MCPLogsMsg requests the captured stderr for a named MCP server. Lines caps
// the number of trailing lines returned per log file (0 = a sensible default).
type MCPLogsMsg struct {
	Name  string `json:"name"`
	Lines int    `json:"lines"`
}

// MCPLogFile is one captured stderr log for an MCP server proxy.
type MCPLogFile struct {
	ProxyID string `json:"proxy_id"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// MCPLogsResponse carries the captured stderr logs for an MCP server.
type MCPLogsResponse struct {
	Name  string       `json:"name"`
	Files []MCPLogFile `json:"files"`
}

type StatusRequestMsg struct {
	SessionID string `json:"session_id"`
}

// Diagnostics types (daemon -> client, in response to "diagnostics" message)

type DiagnosticsMsg struct {
	DaemonPID     int                 `json:"daemon_pid"`
	DaemonVersion string              `json:"daemon_version,omitempty"`
	DaemonUptime  string              `json:"daemon_uptime"`
	Fleet         FleetSummary        `json:"fleet"`
	Sessions      []SessionDiagnostic `json:"sessions"`
	// DeletedSessionIDs identifies soft-deleted sessions without mixing them
	// into the live fleet diagnostics. Doctor uses these IDs to preserve their
	// recoverable on-disk resources when cleaning true orphans.
	DeletedSessionIDs []string             `json:"deleted_session_ids,omitempty"`
	Scrollback        ScrollbackDiagnostic `json:"scrollback"`
	Messages          MessagesDiagnostic   `json:"messages"`
}

type SessionDiagnostic struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	AgentStatus     string `json:"agent_status,omitempty"`
	PID             int    `json:"pid,omitempty"`
	PIDAlive        bool   `json:"pid_alive"`
	HasPTY          *bool  `json:"has_pty,omitempty"`
	WorktreePath    string `json:"worktree_path,omitempty"`
	WorktreeExists  bool   `json:"worktree_exists"`
	ConfigStale     bool   `json:"config_stale"`
	HookStale       bool   `json:"hook_stale"`
	ScrollbackBytes int64  `json:"scrollback_bytes"`
	ScrollbackMax   int64  `json:"scrollback_max"`
	Saturated       bool   `json:"saturated"`
	HasToken        bool   `json:"has_token"`
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

// Scenario messages

type ScenarioStartMsg struct {
	CallerSessionID string                 `json:"caller_session_id"`
	Name            string                 `json:"name"`
	Goal            string                 `json:"goal"`
	Sessions        []ScenarioSessionInput `json:"sessions"`
}

type ScenarioSessionInput struct {
	Name       string `json:"name"`
	Repo       string `json:"repo"`
	Agent      string `json:"agent,omitempty"`
	Model      string `json:"model,omitempty"`
	Base       string `json:"base,omitempty"`
	Role       string `json:"role,omitempty"`
	Task       string `json:"task,omitempty"`
	AgentHooks bool   `json:"agent_hooks,omitempty"`
	Shared     bool   `json:"shared,omitempty"`
}

type ScenarioStopMsg struct {
	Name string `json:"name"`
}

type ScenarioDeleteMsg struct {
	Name string `json:"name"`
}

type ScenarioStatusMsg struct {
	Name string `json:"name"`
}

type ScenarioListMsg struct{}

type ScenarioRecord struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	OrchestratorID string                `json:"orchestrator_id"`
	Goal           string                `json:"goal"`
	Status         string                `json:"status"`
	SessionIDs     []string              `json:"session_ids"`
	Sessions       []ScenarioSessionInfo `json:"sessions"`
	CreatedAt      string                `json:"created_at"`
}

type ScenarioSessionInfo struct {
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
	Role      string `json:"role,omitempty"`
	Task      string `json:"task,omitempty"`
	TaskDone  bool   `json:"task_done,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	Status    string `json:"status,omitempty"`
	Shared    bool   `json:"shared,omitempty"`
}

type ScenarioStatusResponse struct {
	Scenario ScenarioRecord `json:"scenario"`
}

type ScenarioListResponse struct {
	Scenarios []ScenarioRecord `json:"scenarios"`
}

// --- Triggers (control messages: trigger_list/status/run/pause) ---

type TriggerListMsg struct{}

type TriggerStatusMsg struct {
	Name string `json:"name"`
}

type TriggerRunMsg struct {
	Name string `json:"name"`
}

type TriggerPauseMsg struct {
	Name  string `json:"name"`
	Pause bool   `json:"pause"`
}

// TriggerRecord is the per-trigger info returned to the client.
type TriggerRecord struct {
	Name       string `json:"name"`
	Source     string `json:"source"` // schedule | watch
	Action     string `json:"action"` // command | session | scenario | message
	Enabled    bool   `json:"enabled"`
	Paused     bool   `json:"paused,omitempty"`
	Schedule   string `json:"schedule,omitempty"`    // cron/every summary (schedule)
	WatchScope string `json:"watch_scope,omitempty"` // repo/role summary (watch)
	NextFire   string `json:"next_fire,omitempty"`   // schedule
	LastRun    string `json:"last_run,omitempty"`    // RFC3339 or ""
	LastResult string `json:"last_result,omitempty"` // most recent run result
	LastError  string `json:"last_error,omitempty"`
	RunCount   int    `json:"run_count,omitempty"`
	Bindings   int    `json:"bindings,omitempty"` // watch: live bound sessions
	Degraded   string `json:"degraded,omitempty"` // watch: degraded binding reason
}

type TriggerListResponse struct {
	Triggers []TriggerRecord `json:"triggers"`
}

type TriggerStatusResponse struct {
	Trigger TriggerRecord `json:"trigger"`
}

type ScenarioResumeMsg struct {
	Name string `json:"name"`
}

type ScenarioTaskDoneMsg struct {
	Name string `json:"name"`
}

type ScenarioAddMsg struct {
	Name    string               `json:"name"`
	Session ScenarioSessionInput `json:"session"`
}

// Pairing protocol messages (see design §B.2)

// PairRequestMsg is sent by a client to request pairing with the daemon. The
// client supplies a human-readable device label and its device public key.
type PairRequestMsg struct {
	DeviceLabel  string `json:"device_label"`
	DevicePubKey string `json:"device_pub_key"`
}

// PairResponseMsg is the daemon's response to a completed pairing. It returns
// the assigned device ID, a client bearer token, the daemon profile to use, and
// the TLS pin (SPKI) for certificate pinning.
type PairResponseMsg struct {
	DeviceID      string `json:"device_id"`
	ClientToken   string `json:"client_token"`
	DaemonProfile string `json:"daemon_profile"`
	TLSPinSPKI    string `json:"tls_pin_spki"`
}

// PairApproveMsg approves a pending pairing request by its request ID.
type PairApproveMsg struct {
	RequestID string `json:"request_id"`
}

// PairListMsg requests the list of pending and paired devices.
type PairListMsg struct{}

// PairListResponseMsg is the daemon's response to pair_list.
type PairListResponseMsg struct {
	Pending []PairPending      `json:"pending"`
	Paired  []PairedDeviceInfo `json:"paired"`
}

// PairPending describes a pending pairing request awaiting approval.
type PairPending struct {
	RequestID   string `json:"request_id"`
	DeviceLabel string `json:"device_label"`
	TailnetUser string `json:"tailnet_user"`
	TailnetNode string `json:"tailnet_node"`
	RequestedAt string `json:"requested_at"`
}

// PairedDeviceInfo describes an already-paired device.
type PairedDeviceInfo struct {
	DeviceID    string `json:"device_id"`
	Label       string `json:"label"`
	TailnetUser string `json:"tailnet_user"`
	TailnetNode string `json:"tailnet_node"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at"`
}

// PairRevokeMsg revokes a paired device by its device ID.
type PairRevokeMsg struct {
	DeviceID string `json:"device_id"`
}

// Proof-of-possession messages (see design §B.2.4)

// AuthChallengeMsg is sent by the daemon to challenge a client to prove
// possession of its device private key by signing the nonce.
type AuthChallengeMsg struct {
	Nonce string `json:"nonce"`
}

// AuthProofMsg is the client's response to an auth challenge: the signature of
// the proof-of-possession signing input (see PoPSigningInput) produced with
// the device private key.
type AuthProofMsg struct {
	DeviceID  string `json:"device_id"`
	Signature string `json:"signature"`
}

// popSigningPrefix is the domain-separation tag for the proof-of-possession
// signing input. Bump the version suffix if the construction ever changes so
// signatures for the old and new formats can never be confused.
const popSigningPrefix = "graith-pop-v1:"

// PoPSigningInput returns the exact bytes a device must sign for
// proof-of-possession, binding the challenge nonce to the TLS channel it is
// presented over (issue #886). The server-certificate SPKI pin (spki, base64
// SHA-256) is mixed into the signed material, so a man-in-the-middle relaying
// the pairing/auth handshake — who necessarily terminates TLS with a different
// certificate, hence a different SPKI — cannot relay a captured signature: the
// daemon verifies against its own pin and the relayed proof will not match.
//
// Both the daemon (verifyPoP) and every client (Go completeRemotePoP, Swift
// DeviceKeySigner.proof) must build this input identically, byte for byte. The
// client feeds its own observed/pinned SPKI here, never one the peer reports,
// so an attacker cannot talk it into signing over the honest daemon's pin.
//
// Format: "graith-pop-v1:" + nonce + ":" + spki. nonce is the hex challenge
// string and spki is base64 (std) — neither alphabet contains ':' — so the
// delimiters are unambiguous.
func PoPSigningInput(nonce, spki string) []byte {
	return []byte(popSigningPrefix + nonce + ":" + spki)
}

// ApprovalSubscribeMsg subscribes the client to approval notifications.
type ApprovalSubscribeMsg struct{}

// Remote-create repo picker messages (see design §C.4)

// RepoListMsg requests the list of repositories available for session creation.
type RepoListMsg struct{}

// RepoListResponseMsg is the daemon's response to repo_list.
type RepoListResponseMsg struct {
	Repos []RepoEntry `json:"repos"`
}

// RepoEntry describes a repository available for session creation.
type RepoEntry struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Recent bool   `json:"recent,omitempty"`
}
