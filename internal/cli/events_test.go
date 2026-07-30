package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestPrintEvent(t *testing.T) {
	tests := map[string]struct {
		event protocol.EventMsg
		want  string
	}{
		"status change": {
			event: protocol.EventMsg{
				Type:       "status_change",
				At:         "2026-07-29T12:00:00Z",
				SessionID:  "braw1",
				Session:    "braw",
				StatusKind: "agent",
				From:       "active",
				To:         "ready",
			},
			want: "[2026-07-29T12:00:00Z] braw agent status: active -> ready\n",
		},
		"message": {
			event: protocol.EventMsg{
				Type:     "message",
				At:       "2026-07-29T12:01:00Z",
				Topic:    "blether/topic",
				SenderID: "canny1",
				Sender:   "canny",
				Body:     "braw news",
			},
			want: "[2026-07-29T12:01:00Z] message blether/topic from canny: braw news\n",
		},
		"session deleted": {
			event: protocol.EventMsg{
				Type:      "session_deleted",
				At:        "2026-07-29T12:02:00Z",
				SessionID: "dreich1",
				Session:   "dreich",
			},
			want: "[2026-07-29T12:02:00Z] session deleted: dreich\n",
		},
		"forwarded ci": {
			event: protocol.EventMsg{
				Type:            "session_event",
				At:              "2026-07-29T12:03:00Z",
				Forwarded:       true,
				EventClass:      "ci",
				SourceSessionID: "bairn1",
				SourceSession:   "bairn",
				PRNumber:        1646,
				CIState:         "failing",
			},
			want: "[2026-07-29T12:03:00Z] forwarded ci from bairn: failing on PR #1646\n",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			payload, err := json.Marshal(test.event)
			if err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			if err := printEvent(&buf, payload); err != nil {
				t.Fatalf("printEvent: %v", err)
			}

			if got := buf.String(); got != test.want {
				t.Fatalf("printEvent() = %q, want %q", got, test.want)
			}
		})
	}
}
