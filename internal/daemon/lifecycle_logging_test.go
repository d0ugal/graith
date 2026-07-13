package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// newLogCapturingManager builds a SessionManager whose logger writes JSON
// records (at debug level, so audit lines are captured) into the returned
// buffer.
func newLogCapturingManager(t *testing.T) (*SessionManager, *syncBuffer) {
	t.Helper()

	dir := t.TempDir()
	buf := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	sm := NewSessionManager(config.Default(), config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
	}, log)

	return sm, buf
}

// logRecords parses each JSON line emitted into the buffer.
func logRecords(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()

	var records []map[string]any

	for _, line := range bytes.Split([]byte(buf.String()), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}

		records = append(records, rec)
	}

	return records
}

// findRecord returns the first log record with the given msg, or nil.
func findRecord(records []map[string]any, msg string) map[string]any {
	for _, rec := range records {
		if rec["msg"] == msg {
			return rec
		}
	}

	return nil
}

func TestAuthContextDescribe(t *testing.T) {
	tests := []struct {
		name string
		ac   authContext
		want string
	}{
		{"local human", authContext{role: roleLocalHuman}, "local-human"},
		{"remote human", authContext{role: roleRemoteHuman, deviceID: "dev-canny"}, "remote-human(dev-canny)"},
		{"remote guest", authContext{role: roleRemoteGuest, deviceID: "dev-haar"}, "remote-guest(dev-haar)"},
		{"orchestrator", authContext{role: roleOrchestrator, sessionID: "ben-01"}, "orchestrator(ben-01)"},
		{"session", authContext{role: roleSession, sessionID: "braw-02"}, "session(braw-02)"},
		{"none", authContext{role: roleNone}, "unauthenticated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ac.describe(); got != tt.want {
				t.Errorf("describe() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuthContextDescribeOmitsToken(t *testing.T) {
	// A leaked identity string must never carry the bearer token: describe()
	// only renders the role and the session/device id.
	ac := authContext{role: roleSession, sessionID: "braw-02"}
	if got := ac.describe(); got != "session(braw-02)" {
		t.Errorf("describe() = %q, want session(braw-02)", got)
	}
}

func TestPeakRSSProcLabel(t *testing.T) {
	if got := peakRSSProcLabel(true); got != "sandbox-wrapper" {
		t.Errorf("peakRSSProcLabel(true) = %q, want sandbox-wrapper", got)
	}

	if got := peakRSSProcLabel(false); got != "agent" {
		t.Errorf("peakRSSProcLabel(false) = %q, want agent", got)
	}
}

func TestLogStoppingFields(t *testing.T) {
	sm, buf := newLogCapturingManager(t)

	sess := newTestPTYSession(t, "sleep", "100")

	sm.logStopping("braw-01", "braw", StopReasonUser, "user-stop", sess)

	rec := findRecord(logRecords(t, buf), "stopping session")
	if rec == nil {
		t.Fatal("no \"stopping session\" record emitted")
	}

	if rec["id"] != "braw-01" || rec["name"] != "braw" {
		t.Errorf("id/name = %v/%v, want braw-01/braw", rec["id"], rec["name"])
	}

	if rec["reason"] != StopReasonUser || rec["initiator"] != "user-stop" {
		t.Errorf("reason/initiator = %v/%v, want user/user-stop", rec["reason"], rec["initiator"])
	}

	// pid and pgid must be present, non-zero, and equal (Setsid group leader).
	pid, pgid := rec["pid"], rec["pgid"]
	if pid == nil || pgid == nil {
		t.Fatalf("pid/pgid missing: pid=%v pgid=%v", pid, pgid)
	}

	if pid.(float64) <= 0 || pid != pgid {
		t.Errorf("pid=%v pgid=%v, want equal and > 0", pid, pgid)
	}
}

func TestLogStoppingNilSession(t *testing.T) {
	sm, buf := newLogCapturingManager(t)

	// A nil PTY (e.g. an orphaned-process teardown) must not panic and logs pid 0.
	sm.logStopping("dreich-01", "", "rollback", "create-rollback", nil)

	rec := findRecord(logRecords(t, buf), "stopping session")
	if rec == nil {
		t.Fatal("no \"stopping session\" record emitted")
	}

	if rec["pid"].(float64) != 0 || rec["pgid"].(float64) != 0 {
		t.Errorf("pid/pgid = %v/%v, want 0/0 for nil session", rec["pid"], rec["pgid"])
	}
}

// TestStopWithReasonLogsStopping is the regression test for gap #2: a user stop
// must emit a "stopping session" line carrying reason + initiator + pid before
// the SIGTERM is sent.
func TestStopWithReasonLogsStopping(t *testing.T) {
	sm, buf := newLogCapturingManager(t)

	id := "braw-stop"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
	}

	sess := newTestPTYSession(t, "sleep", "100")
	sm.sessions[id] = sess

	if err := sm.Stop(id); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	rec := findRecord(logRecords(t, buf), "stopping session")
	if rec == nil {
		t.Fatal("Stop did not emit a \"stopping session\" record")
	}

	if rec["reason"] != StopReasonUser || rec["initiator"] != "user-stop" {
		t.Errorf("reason/initiator = %v/%v, want user/user-stop", rec["reason"], rec["initiator"])
	}

	if rec["pid"] == nil || rec["pid"].(float64) <= 0 {
		t.Errorf("pid = %v, want > 0", rec["pid"])
	}
}

// TestWatchSessionExitLogFields is the regression test for gaps #1 and #4: the
// "session exited" line must carry stop_reason and pid/pgid.
func TestWatchSessionExitLogFields(t *testing.T) {
	sm, buf := newLogCapturingManager(t)

	id := "braw-exit"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
		StopReason: StopReasonUser,
	}

	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)

	sm.sessions[id] = sess
	sm.watchSession(id, sess)

	rec := findRecord(logRecords(t, buf), "session exited")
	if rec == nil {
		t.Fatal("no \"session exited\" record emitted")
	}

	if rec["stop_reason"] != StopReasonUser {
		t.Errorf("stop_reason = %v, want %q", rec["stop_reason"], StopReasonUser)
	}

	pid, pgid := rec["pid"], rec["pgid"]
	if pid == nil || pgid == nil {
		t.Fatalf("pid/pgid missing on exit line: pid=%v pgid=%v", pid, pgid)
	}

	if pid.(float64) <= 0 || pid != pgid {
		t.Errorf("pid=%v pgid=%v, want equal and > 0", pid, pgid)
	}
}

// TestWatchSessionExitDefaultsToCrash covers the fallback: a process that exits
// with no recorded stop reason is attributed to a crash.
func TestWatchSessionExitDefaultsToCrash(t *testing.T) {
	sm, buf := newLogCapturingManager(t)

	id := "dreich-exit"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "dreich", Status: StatusRunning, Agent: "claude",
	}

	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)

	sm.sessions[id] = sess
	sm.watchSession(id, sess)

	rec := findRecord(logRecords(t, buf), "session exited")
	if rec == nil {
		t.Fatal("no \"session exited\" record emitted")
	}

	if rec["stop_reason"] != StopReasonCrash {
		t.Errorf("stop_reason = %v, want %q", rec["stop_reason"], StopReasonCrash)
	}
}

// TestSessionActiveLaunchTiming is the regression test for gap #6: a
// SessionStart hook report emits a "session active" line with the
// launch→active duration.
func TestSessionActiveLaunchTiming(t *testing.T) {
	sm, buf := newLogCapturingManager(t)

	id := "braw-active"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
	}

	sess := newTestPTYSession(t, "sleep", "100")
	sm.sessions[id] = sess

	sm.HandleHookReport(protocol.StatusReportMsg{SessionID: id, Event: "SessionStart"})

	rec := findRecord(logRecords(t, buf), "session active")
	if rec == nil {
		t.Fatal("SessionStart did not emit a \"session active\" record")
	}

	if rec["session_id"] != id {
		t.Errorf("session_id = %v, want %q", rec["session_id"], id)
	}

	if _, ok := rec["since_launch_ms"]; !ok {
		t.Error("session active record missing since_launch_ms")
	}
}
