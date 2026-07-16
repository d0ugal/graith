// Package headless drives a Claude Code agent in headless stream-json mode
// (`claude -p --output-format stream-json`) instead of an interactive PTY. It
// parses the typed event stream for status and the terminal result cost/usage
// envelope. See docs/design/2026-07-13-headless-stream-json-design.md (#1075).
//
// v1 runs the one-shot control-channel form: `claude -p --output-format
// stream-json --input-format stream-json --verbose --permission-prompt-tool
// stdio`. The prompt is delivered as an initial stdin user message (not a
// positional arg), the stdin channel stays open for the turn so graith can
// issue an `interrupt` control request and answer inbound `can_use_tool`
// permission asks, and stdin is closed on the terminal `result` so the process
// exits (preserving one-shot semantics). See issue #1136 (Phase 4).
//
// The control protocol is an SDK-internal contract, not a documented CLI API,
// so everything is written defensively (unknown message types ignored,
// malformed data lines skipped) and the whole feature is gated behind an
// experimental flag by the daemon. The wire shapes here are pinned to the forms
// empirically verified against claude 2.1.211 — notably the *asymmetric*
// request-id placement (see below).
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
//
// Control-frame request ids are ASYMMETRIC (verified against claude 2.1.211):
//   - An inbound control_request (CLI→graith, e.g. can_use_tool) carries
//     request_id at the TOP LEVEL — this RequestID field.
//   - A control_response (CLI→graith, e.g. the interrupt reply) nests
//     request_id (and a success/error subtype) INSIDE "response"; see
//     controlResponse below, decoded from the Response raw field.
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

	// control protocol fields (type == "control_request"/"control_response").
	// RequestID is the top-level id of an inbound control_request; a
	// control_response's id lives inside Response (see controlResponse).
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
}

// controlResponse is the CLI's reply to a control request, decoded from an
// event's "response" object. request_id and the protocol-level success/error
// subtype are nested here (NOT at the event top level); the actual payload is
// one level deeper in Payload (e.g. {"still_queued":[]} for interrupt).
//
//	{"type":"control_response","response":{"subtype":"success",
//	 "request_id":"req-1","response":{...payload...}}}
type controlResponse struct {
	Subtype   string          `json:"subtype"` // "success" | "error"
	RequestID string          `json:"request_id"`
	Payload   json.RawMessage `json:"response"`
	Error     string          `json:"error"`
}

// canUseToolRequest is the body of an inbound can_use_tool control request: the
// CLI asking graith to approve a tool call.
type canUseToolRequest struct {
	Subtype  string          `json:"subtype"`
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
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
