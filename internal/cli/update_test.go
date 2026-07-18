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

func boolptr(v bool) *bool { return &v }

func updatedResp(id, name, parentID string, starred bool) scriptedResp {
	return okResp(payloadEnv("updated", protocol.UpdateResultMsg{
		SessionID: id,
		Name:      name,
		ParentID:  parentID,
		Starred:   starred,
	}))
}

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
		{name: "valid starred false", opts: updateOptions{starred: boolptr(false)}},
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
			okResp(payloadEnv("session_list", protocol.SessionListMsg{})),
			updatedResp("id-braw", "bonnie", "", false),
		}}

		if err := runUpdate(c, "braw", updateOptions{name: strptr("bonnie")}); err != nil {
			t.Fatalf("runUpdate: %v", err)
		}

		if got := c.sentTypes(); len(got) != 3 || got[0] != "list" || got[1] != "list" || got[2] != "update" {
			t.Fatalf("sent = %v, want [list list update]", got)
		}

		msg, ok := c.sends[2].Payload.(protocol.UpdateMsg)
		if !ok || msg.SessionID != "id-braw" || msg.Name == nil || *msg.Name != "bonnie" || msg.ParentID != nil || msg.Starred != nil {
			t.Fatalf("payload = %+v, want name-only update", c.sends[2].Payload)
		}

		if got := buf.String(); got != "Name: bonnie\n" {
			t.Fatalf("output = %q", got)
		}
	})

	t.Run("JSON output is one parseable result", func(t *testing.T) {
		buf := captureUpdateOutput(t, true)
		c := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "id-braw", Name: "braw"}}})),
			updatedResp("id-braw", "bonnie", "", false),
		}}

		if err := runUpdate(c, "id-braw", updateOptions{name: strptr("bonnie")}); err != nil {
			t.Fatalf("runUpdate: %v", err)
		}

		var got protocol.UpdateResultMsg
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
		}

		if got.SessionID != "id-braw" || got.Name != "bonnie" || got.ParentID != "" || got.Starred {
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

		c := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", duplicateNames)),
			okResp(payloadEnv("session_list", protocol.SessionListMsg{})),
		}}

		err := runUpdate(c, "dreich", updateOptions{name: strptr("bonnie")})
		if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "use an explicit ID") {
			t.Fatalf("error = %v, want explicit ambiguity guidance", err)
		}

		if got := c.sentTypes(); len(got) != 2 || got[0] != "list" || got[1] != "list" {
			t.Fatalf("sent = %v, want live and deleted list lookups", got)
		}
	})

	t.Run("exact ID works with duplicate names", func(t *testing.T) {
		captureUpdateOutput(t, false)

		c := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", duplicateNames)),
			updatedResp("id-dreich-2", "bonnie", "", false),
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

	t.Run("deleted exact ID takes precedence over a live colliding name", func(t *testing.T) {
		captureUpdateOutput(t, false)

		c := &scriptedConn{responses: []scriptedResp{
			okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
				{ID: "id-canny", Name: "id-auld"},
			}})),
			okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
				{ID: "id-auld", Name: "auld"},
			}})),
		}}

		err := runUpdate(c, "id-auld", updateOptions{name: strptr("bonnie")})
		if err == nil || !strings.Contains(err.Error(), "soft-deleted") || !strings.Contains(err.Error(), "gr restore") {
			t.Fatalf("error = %v, want restore guidance for exact deleted ID", err)
		}

		if got := c.sentTypes(); len(got) != 2 || got[0] != "list" || got[1] != "list" {
			t.Fatalf("sent = %v, want only live and deleted list lookups", got)
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
		okResp(payloadEnv("session_list", protocol.SessionListMsg{})),
		okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
			{ID: "id-bairn", Name: "bairn"},
			{ID: "id-ben", Name: "ben"},
		}})),
		okResp(payloadEnv("session_list", protocol.SessionListMsg{})),
		updatedResp("id-bairn", "bonnie", "id-ben", false),
	}}

	if err := runUpdate(c, "bairn", updateOptions{name: strptr("bonnie"), parent: strptr("ben"), starred: boolptr(false)}); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}

	msg := c.sends[4].Payload.(protocol.UpdateMsg)
	if msg.ParentID == nil || *msg.ParentID != "id-ben" {
		t.Fatalf("parent_id = %v, want id-ben", msg.ParentID)
	}
	if msg.Starred == nil || *msg.Starred {
		t.Fatalf("starred = %v, want explicit false", msg.Starred)
	}

	var result protocol.UpdateResultMsg
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	if result.ParentID != "id-ben" || result.Starred {
		t.Fatalf("JSON parent_id = %v, want id-ben", result.ParentID)
	}
}

func TestRunUpdateDaemonError(t *testing.T) {
	captureUpdateOutput(t, false)

	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "id-braw", Name: "braw"}}})),
		okResp(payloadEnv("session_list", protocol.SessionListMsg{})),
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

func TestRunUpdateStarredFalseReportsResult(t *testing.T) {
	buf := captureUpdateOutput(t, false)
	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("session_list", protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "id-braw", Name: "braw", Starred: true}}})),
		updatedResp("id-braw", "braw", "", false),
	}}

	if err := runUpdate(c, "id-braw", updateOptions{starred: boolptr(false)}); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}

	msg := c.sends[1].Payload.(protocol.UpdateMsg)
	if msg.Starred == nil || *msg.Starred {
		t.Fatalf("starred = %v, want explicit false", msg.Starred)
	}
	if got := buf.String(); got != "Starred: false\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestUpdateStarredFlagAndRemovedCommands(t *testing.T) {
	registerCommands()

	flag := updateCmd.Flags().Lookup("starred")
	if flag == nil {
		t.Fatal("update --starred flag is not registered")
	}
	if flag.NoOptDefVal != "true" {
		t.Errorf("--starred NoOptDefVal = %q, want true", flag.NoOptDefVal)
	}

	originalValue, originalChanged := flag.Value.String(), flag.Changed
	t.Cleanup(func() {
		_ = flag.Value.Set(originalValue)
		flag.Changed = originalChanged
	})

	if err := flag.Value.Set("false"); err != nil {
		t.Fatalf("setting --starred=false: %v", err)
	}
	if got, _ := updateCmd.Flags().GetBool("starred"); got {
		t.Error("--starred=false did not remain explicit false")
	}

	for _, removed := range []string{"star", "unstar"} {
		for _, cmd := range rootCmd.Commands() {
			if cmd.Name() == removed {
				t.Errorf("removed top-level command %q is still registered", removed)
			}
		}
	}
}
