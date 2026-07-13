// Package headless drives a Claude Code agent in headless stream-json mode
// (`claude -p --output-format stream-json --input-format stream-json`) instead
// of an interactive PTY. It parses the typed event stream for status, cost and
// token usage, and speaks the control protocol (initialize, interrupt,
// can_use_tool approvals) on stdin/stdout. See
// docs/design/2026-07-13-headless-stream-json-design.md (issue #1075).
//
// The control protocol on stdin is an SDK-internal contract, not a documented
// CLI API, so everything here is written defensively (unknown message types are
// ignored, malformed data lines skipped) and the feature is gated behind an
// experimental flag by the daemon.
package headless

import (
	"encoding/json"
	"time"
)

// Status is the coarse agent status graith tracks, mirroring the strings the
// detector/hook path uses so the daemon can treat headless and PTY sessions
// uniformly.
type Status string

const (
	StatusActive   Status = "active"
	StatusApproval Status = "approval"
	StatusReady    Status = "ready"
	StatusStopped  Status = "stopped"
)

// event is a single stream-json line. Only the fields graith consumes are
// decoded; the rest of each line is kept raw for rendering.
type event struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`

	// result envelope fields (type == "result")
	IsError     *bool           `json:"is_error"`
	TotalCost   *float64        `json:"total_cost_usd"`
	NumTurns    *int            `json:"num_turns"`
	DurationMS  *int64          `json:"duration_ms"`
	DurationAPI *int64          `json:"duration_api_ms"`
	Usage       json.RawMessage `json:"usage"`
	ResultText  string          `json:"result"`

	// control protocol fields (type == "control_request"/"control_response")
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
}

// assistantMessage is the nested `message` of an assistant event, decoded only
// far enough to render text and spot tool use.
type assistantMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Name string `json:"name"`
	} `json:"content"`
}

// ResultEnvelope is the terminal `result` message: the structured cost/usage
// summary the design feeds into token accounting (#644). A one-shot headless
// session emits exactly one of these.
type ResultEnvelope struct {
	IsError     bool            `json:"is_error"`
	TotalCost   float64         `json:"total_cost_usd"`
	NumTurns    int             `json:"num_turns"`
	DurationMS  int64           `json:"duration_ms"`
	DurationAPI int64           `json:"duration_api_ms"`
	Usage       json.RawMessage `json:"usage"`
	Text        string          `json:"result"`
	At          time.Time       `json:"-"`
}

// Snapshot is the structured status the daemon reads off a headless driver in
// place of PTY scraping.
type Snapshot struct {
	Status   Status
	ToolName string
	Result   *ResultEnvelope
	Degraded bool // reader hit malformed control frames / decode failures
}

// --- control protocol -------------------------------------------------------

// controlRequest is a request graith sends to the CLI on stdin.
type controlRequest struct {
	Type      string `json:"type"` // "control_request"
	RequestID string `json:"request_id"`
	Request   any    `json:"request"`
}

// initializeRequest / interruptRequest / contextUsageRequest are the request
// bodies for the control subtypes graith uses in v1.
type controlSubtype struct {
	Subtype string `json:"subtype"`
}

// PermissionRequest is an inbound can_use_tool control request: the CLI asking
// graith to approve a tool call.
type PermissionRequest struct {
	RequestID string
	ToolName  string
	// Input is the raw tool input JSON, passed through to the approval backend.
	Input json.RawMessage
}

// PermissionDecision is graith's answer to a PermissionRequest.
type PermissionDecision struct {
	Allow bool
	// Reason is surfaced to the agent on deny.
	Reason string
}

// userMessage is the SDKUserMessage graith writes to feed a prompt/turn.
type userMessage struct {
	Type    string `json:"type"` // "user"
	Message any    `json:"message"`
}

// statusForEvent maps a decoded event to a graith status, or "" if the event
// does not change status. Pure function so it is trivially unit-testable.
func statusForEvent(ev event) (Status, bool) {
	switch ev.Type {
	case "system":
		return StatusActive, true
	case "assistant", "user":
		return StatusActive, true
	case "result":
		return StatusReady, true
	case "control_request":
		// An inbound control_request from the CLI is (in v1) a can_use_tool
		// permission ask — the agent is blocked awaiting a decision.
		if controlSubtypeOf(ev.Request) == "can_use_tool" {
			return StatusApproval, true
		}
	}

	return "", false
}

// controlSubtypeOf extracts the "subtype" from a raw control request/response
// body, tolerating absence.
func controlSubtypeOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s controlSubtype
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}

	return s.Subtype
}
