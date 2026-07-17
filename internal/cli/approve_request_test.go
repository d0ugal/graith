package cli

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

type fakeApprovalControlConn struct {
	sendErr error
	readErr error
	resp    protocol.Envelope
	sent    bool
	read    bool
}

func (c *fakeApprovalControlConn) SendControl(string, any) error {
	c.sent = true
	return c.sendErr
}

func (c *fakeApprovalControlConn) ReadControlResponse() (protocol.Envelope, error) {
	c.read = true
	return c.resp, c.readErr
}

func approvalEnvelope(t *testing.T, decision, reason string) protocol.Envelope {
	t.Helper()

	payload, err := json.Marshal(protocol.ApprovalDecisionMsg{Decision: decision, Reason: reason})
	if err != nil {
		t.Fatal(err)
	}

	return protocol.Envelope{Type: "approval_decision", Payload: payload}
}

func TestSubmitApprovalRequestChecksSendError(t *testing.T) {
	c := &fakeApprovalControlConn{sendErr: errors.New("dreich write failure")}
	out := submitApprovalRequest(c, "claude", protocol.ApprovalRequestMsg{RequestID: "canny"})

	if !c.sent || c.read {
		t.Errorf("send/read calls = %v/%v, want send then stop", c.sent, c.read)
	}

	for _, want := range []string{"permissionDecision\":\"allow", "send request failed", "dreich write failure", "fail-open hook policy"} {
		if !strings.Contains(out, want) {
			t.Errorf("send-error hook output %q missing %q", out, want)
		}
	}
}

func TestSubmitApprovalRequestMakesOperationTimeoutVisible(t *testing.T) {
	c := &fakeApprovalControlConn{readErr: os.ErrDeadlineExceeded}
	out := submitApprovalRequest(c, "claude", protocol.ApprovalRequestMsg{RequestID: "canny"})

	for _, want := range []string{"permissionDecision\":\"allow", "approval operation timed out", "wait for response", "fail-open hook policy"} {
		if !strings.Contains(out, want) {
			t.Errorf("timeout hook output %q missing %q", out, want)
		}
	}
}

func TestSubmitApprovalRequestReturnsDaemonDecision(t *testing.T) {
	c := &fakeApprovalControlConn{resp: approvalEnvelope(t, "block", "canny policy")}
	out := submitApprovalRequest(c, "claude", protocol.ApprovalRequestMsg{RequestID: "canny"})

	for _, want := range []string{"permissionDecision\":\"deny", "canny policy"} {
		if !strings.Contains(out, want) {
			t.Errorf("decision hook output %q missing %q", out, want)
		}
	}
}
