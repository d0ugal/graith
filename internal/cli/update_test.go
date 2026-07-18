package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
)

func strptr(s string) *string { return &s }

func captureUpdateOutput(t *testing.T, jsonMode bool) *bytes.Buffer {
	t.Helper()

	origOut, origJSON := out, jsonOutput
	buf := &bytes.Buffer{}
	out = output.NewWithWriter(jsonMode, buf)
	jsonOutput = jsonMode

	t.Cleanup(func() {
		out = origOut
		jsonOutput = origJSON
	})

	return buf
}

func TestValidateUpdateOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    updateOptions
		wantErr string
	}{
		{name: "no properties", wantErr: "at least one"},
		{name: "valid name", opts: updateOptions{name: strptr("bonnie")}},
		{name: "valid orphan", opts: updateOptions{parent: strptr("")}},
		{name: "invalid name", opts: updateOptions{name: strptr("bad name/slash")}, wantErr: "invalid"},
		{name: "reserved name", opts: updateOptions{name: strptr("orchestrator")}, wantErr: "reserved for system use"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUpdateOptions(tt.opts)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want text %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunUpdateName(t *testing.T) {
	t.Run("human output and update payload", func(t *testing.T) {
		buf := captureUpdateOutput(t, false)
		c := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "id-braw", Name: "braw"}}})),
			okResp(typeEnv("updated")),
		}}

		if err := runUpdate(c, "braw", updateOptions{name: strptr("bonnie")}); err != nil {
			t.Fatalf("runUpdate: %v", err)
		}

		if got := c.sentTypes(); len(got) != 2 || got[0] != "list" || got[1] != "update" {
			t.Fatalf("sent = %v, want [list update]", got)
		}

		msg, ok := c.sends[1].Payload.(protocol.UpdateMsg)
		if !ok || msg.SessionID != "id-braw" || msg.Name == nil || *msg.Name != "bonnie" || msg.ParentID != nil {
			t.Fatalf("payload = %+v, want name-only update", c.sends[1].Payload)
		}

		if got := buf.String(); got != "Name updated to bonnie\n" {
			t.Fatalf("output = %q", got)
		}
	})

	t.Run("JSON output is one parseable result", func(t *testing.T) {
		buf := captureUpdateOutput(t, true)
		c := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "id-braw", Name: "braw"}}})),
			okResp(typeEnv("updated")),
		}}

		if err := runUpdate(c, "id-braw", updateOptions{name: strptr("bonnie")}); err != nil {
			t.Fatalf("runUpdate: %v", err)
		}

		var got updateOutput
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
		}

		if got.SessionID != "id-braw" || got.Name == nil || *got.Name != "bonnie" || got.ParentID != nil {
			t.Fatalf("JSON result = %+v", got)
		}
	})
}

func TestRunUpdateNameResolution(t *testing.T) {
	duplicateNames := protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
		{ID: "id-dreich-1", Name: "dreich"},
		{ID: "id-dreich-2", Name: "dreich"},
	}}

	t.Run("duplicate name is explicitly ambiguous", func(t *testing.T) {
		captureUpdateOutput(t, false)
		c := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("session_list", duplicateNames))}}

		err := runUpdate(c, "dreich", updateOptions{name: strptr("bonnie")})
		if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "use an explicit ID") {
			t.Fatalf("error = %v, want explicit ambiguity guidance", err)
		}

		if got := c.sentTypes(); len(got) != 1 || got[0] != "list" {
			t.Fatalf("sent = %v, want only list", got)
		}
	})

	t.Run("exact ID works with duplicate names", func(t *testing.T) {
		captureUpdateOutput(t, false)
		c := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", duplicateNames)),
			okResp(typeEnv("updated")),
		}}

		if err := runUpdate(c, "id-dreich-2", updateOptions{name: strptr("bonnie")}); err != nil {
			t.Fatalf("runUpdate: %v", err)
		}

		msg := c.sends[1].Payload.(protocol.UpdateMsg)
		if msg.SessionID != "id-dreich-2" {
			t.Fatalf("session_id = %q, want id-dreich-2", msg.SessionID)
		}
	})

	t.Run("soft-deleted target has restore guidance", func(t *testing.T) {
		captureUpdateOutput(t, false)
		c := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", protocol.SessionListMsg{})),
			okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "id-auld", Name: "auld"}}})),
		}}

		err := runUpdate(c, "id-auld", updateOptions{name: strptr("bonnie")})
		if err == nil || !strings.Contains(err.Error(), "soft-deleted") || !strings.Contains(err.Error(), "gr restore") {
			t.Fatalf("error = %v, want restore guidance", err)
		}
	})
}

func TestRunUpdateCombinedProperties(t *testing.T) {
	buf := captureUpdateOutput(t, true)
	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
			{ID: "id-bairn", Name: "bairn"},
			{ID: "id-ben", Name: "ben"},
		}})),
		okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
			{ID: "id-bairn", Name: "bairn"},
			{ID: "id-ben", Name: "ben"},
		}})),
		okResp(typeEnv("updated")),
	}}

	if err := runUpdate(c, "bairn", updateOptions{name: strptr("bonnie"), parent: strptr("ben")}); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}

	msg := c.sends[2].Payload.(protocol.UpdateMsg)
	if msg.ParentID == nil || *msg.ParentID != "id-ben" {
		t.Fatalf("parent_id = %v, want id-ben", msg.ParentID)
	}

	var result updateOutput
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	if result.ParentID == nil || *result.ParentID != "id-ben" {
		t.Fatalf("JSON parent_id = %v, want id-ben", result.ParentID)
	}
}

func TestRunUpdateDaemonError(t *testing.T) {
	captureUpdateOutput(t, false)
	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "id-braw", Name: "braw"}}})),
		okResp(errEnv("cannot update system session \"orchestrator\"")),
	}}

	err := runUpdate(c, "braw", updateOptions{name: strptr("bonnie")})
	if err == nil || err.Error() != "cannot update system session \"orchestrator\"" {
		t.Fatalf("error = %v", err)
	}
}

func TestRunUpdateInvalidNameDoesNotUseConnection(t *testing.T) {
	captureUpdateOutput(t, false)
	c := &scriptedConn{responses: []scriptedResp{errResp(io.ErrUnexpectedEOF)}}

	if err := runUpdate(c, "braw", updateOptions{name: strptr("bad name/slash")}); err == nil {
		t.Fatal("expected validation error")
	}

	if len(c.sends) != 0 || c.readIdx != 0 {
		t.Fatalf("invalid name used connection: sends=%v reads=%d", c.sentTypes(), c.readIdx)
	}
}
