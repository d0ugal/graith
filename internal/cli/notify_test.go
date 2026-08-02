package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
)

func setupNotifyRunTest(t *testing.T, jsonMode bool, buf *bytes.Buffer, conn *scriptedConn) {
	t.Helper()

	origOut := out
	origJSON := jsonOutput
	origTitle := notifyTitle
	origPriority := notifyPriority
	origConnect := notifyConnectFn

	out = output.NewWithWriter(jsonMode, buf)
	jsonOutput = jsonMode
	notifyTitle = "graith"
	notifyPriority = "low"
	notifyConnectFn = func(*config.Config, config.Paths, string) (notifyConn, error) {
		return conn, nil
	}

	t.Cleanup(func() {
		out = origOut
		jsonOutput = origJSON
		notifyTitle = origTitle
		notifyPriority = origPriority
		notifyConnectFn = origConnect
	})
}

func TestRunNotifyJSONDeliveredResponse(t *testing.T) {
	var buf bytes.Buffer

	conn := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("notify_response", protocol.NotifyResponse{Delivered: true})),
	}}
	setupNotifyRunTest(t, true, &buf, conn)

	if err := runNotify([]string{"braw", "briefing"}); err != nil {
		t.Fatalf("runNotify() = %v", err)
	}

	assertNotifyJSON(t, buf.Bytes(), notifyJSONResponse{Delivered: true, Reason: ""})
	assertNotifySend(t, conn, "braw briefing")
}

func TestRunNotifyJSONNotDeliveredResponse(t *testing.T) {
	var buf bytes.Buffer

	conn := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("notify_response", protocol.NotifyResponse{
			Delivered: false,
			Reason:    "backend \"macos\" dispatch failed: native notifier GraithNotifier.app not found",
		})),
	}}
	setupNotifyRunTest(t, true, &buf, conn)

	if err := runNotify([]string{"dreich"}); err != nil {
		t.Fatalf("runNotify() = %v", err)
	}

	assertNotifyJSON(t, buf.Bytes(), notifyJSONResponse{
		Delivered: false,
		Reason:    "backend \"macos\" dispatch failed: native notifier GraithNotifier.app not found",
	})
	assertNotifySend(t, conn, "dreich")
}

func TestRunNotifyHumanResponses(t *testing.T) {
	tests := map[string]struct {
		response protocol.NotifyResponse
		want     string
	}{
		"delivered":     {response: protocol.NotifyResponse{Delivered: true}, want: "Notification sent\n"},
		"not delivered": {response: protocol.NotifyResponse{Reason: "notifications are disabled"}, want: "Notification not delivered: notifications are disabled\n"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer

			conn := &scriptedConn{responses: []scriptedResp{
				okResp(payloadEnv("notify_response", test.response)),
			}}
			setupNotifyRunTest(t, false, &buf, conn)

			if err := runNotify([]string{"canny"}); err != nil {
				t.Fatalf("runNotify() = %v", err)
			}

			if got := buf.String(); got != test.want {
				t.Fatalf("human notify output = %q, want %q", got, test.want)
			}

			assertNotifySend(t, conn, "canny")
		})
	}
}

func assertNotifyJSON(t *testing.T, data []byte, want notifyJSONResponse) {
	t.Helper()

	if len(bytes.TrimSpace(data)) == 0 {
		t.Fatal("notify --json wrote empty stdout")
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("notify --json stdout is not valid JSON: %v\n%s", err, data)
	}

	if _, ok := raw["delivered"]; !ok {
		t.Fatalf("notify --json missing delivered field: %s", data)
	}

	if _, ok := raw["reason"]; !ok {
		t.Fatalf("notify --json missing reason field: %s", data)
	}

	var got notifyJSONResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode notify JSON: %v", err)
	}

	if got != want {
		t.Fatalf("notify JSON = %+v, want %+v", got, want)
	}
}

func assertNotifySend(t *testing.T, conn *scriptedConn, wantMessage string) {
	t.Helper()

	if conn.closed != 1 {
		t.Fatalf("connection closed %d times, want 1", conn.closed)
	}

	if len(conn.sends) != 1 || conn.sends[0].Type != "notify" {
		t.Fatalf("sends = %#v, want one notify", conn.sends)
	}

	payload, ok := conn.sends[0].Payload.(protocol.NotifyMsg)
	if !ok {
		t.Fatalf("notify payload = %T, want protocol.NotifyMsg", conn.sends[0].Payload)
	}

	if payload.Message != wantMessage || payload.Title != "graith" || payload.Priority != "low" {
		t.Fatalf("notify payload = %+v", payload)
	}
}
