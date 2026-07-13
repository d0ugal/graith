package headless

import (
	"encoding/json"
	"testing"
)

func TestStatusForEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ev         event
		wantStatus Status
		wantOK     bool
	}{
		{
			name:       "system is active",
			ev:         event{Type: "system", SessionID: "braw"},
			wantStatus: StatusActive,
			wantOK:     true,
		},
		{
			name:       "assistant is active",
			ev:         event{Type: "assistant"},
			wantStatus: StatusActive,
			wantOK:     true,
		},
		{
			name:       "user is active",
			ev:         event{Type: "user"},
			wantStatus: StatusActive,
			wantOK:     true,
		},
		{
			name:       "result is ready",
			ev:         event{Type: "result"},
			wantStatus: StatusReady,
			wantOK:     true,
		},
		{
			name:       "control_request can_use_tool is approval",
			ev:         event{Type: "control_request", Request: json.RawMessage(`{"subtype":"can_use_tool","tool_name":"Bash"}`)},
			wantStatus: StatusApproval,
			wantOK:     true,
		},
		{
			name:   "control_request other subtype does not change status",
			ev:     event{Type: "control_request", Request: json.RawMessage(`{"subtype":"initialize"}`)},
			wantOK: false,
		},
		{
			name:   "control_request with no request body does not change status",
			ev:     event{Type: "control_request"},
			wantOK: false,
		},
		{
			name:   "control_response does not change status",
			ev:     event{Type: "control_response"},
			wantOK: false,
		},
		{
			name:   "unknown type does not change status",
			ev:     event{Type: "dreich"},
			wantOK: false,
		},
		{
			name:   "empty type does not change status",
			ev:     event{},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := statusForEvent(tt.ev)
			if ok != tt.wantOK {
				t.Fatalf("statusForEvent ok = %v, want %v", ok, tt.wantOK)
			}

			if ok && got != tt.wantStatus {
				t.Fatalf("statusForEvent status = %q, want %q", got, tt.wantStatus)
			}

			if !ok && got != "" {
				t.Fatalf("statusForEvent returned status %q with ok=false, want empty", got)
			}
		})
	}
}

func TestControlSubtypeOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "empty raw", raw: nil, want: ""},
		{name: "zero-length raw", raw: json.RawMessage{}, want: ""},
		{name: "can_use_tool", raw: json.RawMessage(`{"subtype":"can_use_tool"}`), want: "can_use_tool"},
		{name: "interrupt", raw: json.RawMessage(`{"subtype":"interrupt"}`), want: "interrupt"},
		{name: "missing subtype key", raw: json.RawMessage(`{"other":"ken"}`), want: ""},
		{name: "malformed json", raw: json.RawMessage(`{not json`), want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := controlSubtypeOf(tt.raw); got != tt.want {
				t.Fatalf("controlSubtypeOf(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
