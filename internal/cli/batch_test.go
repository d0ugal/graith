package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

func TestParseStaleDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"6h", 6 * time.Hour},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"1d6h", 30 * time.Hour},
		{"30m", 30 * time.Minute},
		{"2d12h30m", 60*time.Hour + 30*time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseStaleDuration(tt.input)
			if err != nil {
				t.Fatalf("parseStaleDuration(%q) error: %v", tt.input, err)
			}

			if got != tt.want {
				t.Errorf("parseStaleDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseStaleDurationErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"garbage", "garbage"},
		{"atoi overflow", "99999999999999999999d"},
		{"day multiplication overflow wraps small", "768614336404564651d"},
		{"zero days", "0d"},
		{"negative days", "-1d"},
		{"negative hours", "-6h"},
		{"zero hours", "0h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseStaleDuration(tt.input); err == nil {
				t.Errorf("parseStaleDuration(%q) expected error, got nil", tt.input)
			}
		})
	}
}

func TestFilterSessions(t *testing.T) {
	now := time.Now()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	old := now.Add(-48 * time.Hour).Format(time.RFC3339)

	sessions := []protocol.SessionInfo{
		{ID: "1", Name: "running-croft", RepoName: "croft", Status: "running", LastAttachedAt: recent},
		{ID: "2", Name: "stopped-croft", RepoName: "croft", Status: "stopped", LastAttachedAt: old},
		{ID: "3", Name: "running-thrawn", RepoName: "thrawn", Status: "running", LastAttachedAt: old},
		{ID: "4", Name: "errored-croft", RepoName: "croft", Status: "errored", LastAttachedAt: old},
		{ID: "5", Name: "never-attached", RepoName: "croft", Status: "stopped", LastAttachedAt: "", CreatedAt: old},
		{ID: "6", Name: "never-attached-recent", RepoName: "croft", Status: "running", LastAttachedAt: "", CreatedAt: recent},
	}

	tests := []struct {
		name    string
		flags   batchFlags
		wantIDs []string
	}{
		{
			name:    "repo filter",
			flags:   batchFlags{repo: "croft"},
			wantIDs: []string{"1", "2", "4", "5", "6"},
		},
		{
			name:    "stopped filter",
			flags:   batchFlags{stopped: true},
			wantIDs: []string{"2", "4", "5"},
		},
		{
			name:    "stale filter includes never-attached",
			flags:   batchFlags{stale: "6h"},
			wantIDs: []string{"2", "3", "4", "5"},
		},
		{
			name:    "repo + stopped",
			flags:   batchFlags{repo: "croft", stopped: true},
			wantIDs: []string{"2", "4", "5"},
		},
		{
			name:    "repo + stopped + stale",
			flags:   batchFlags{repo: "croft", stopped: true, stale: "6h"},
			wantIDs: []string{"2", "4", "5"},
		},
		{
			name:    "stale with no matches",
			flags:   batchFlags{stale: "7d"},
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := filterSessions(sessions, &tt.flags)
			if err != nil {
				t.Fatalf("filterSessions error: %v", err)
			}

			gotIDs := make([]string, len(got))
			for i, s := range got {
				gotIDs[i] = s.ID
			}

			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got %v, want %v", gotIDs, tt.wantIDs)
			}

			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Errorf("got[%d] = %s, want %s", i, gotIDs[i], tt.wantIDs[i])
				}
			}
		})
	}
}

// setDiscardOutForBatch swaps the package out writer for a discarding one in the
// requested JSON mode, restoring the original on cleanup.
func setDiscardOutForBatch(t *testing.T, jsonMode bool) {
	t.Helper()

	orig := out

	t.Cleanup(func() { out = orig })

	out = output.NewWithWriter(jsonMode, io.Discard)
}

// TestBatchFlagsActive2 verifies active() reports true when any single filter is
// set and false only when all are empty.
func TestBatchFlagsActive2(t *testing.T) {
	tests := []struct {
		name string
		bf   batchFlags
		want bool
	}{
		{name: "empty is inactive", bf: batchFlags{}, want: false},
		{name: "repo activates", bf: batchFlags{repo: "croft"}, want: true},
		{name: "stopped activates", bf: batchFlags{stopped: true}, want: true},
		{name: "stale activates", bf: batchFlags{stale: "7d"}, want: true},
		{name: "force alone does not activate", bf: batchFlags{force: true}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.bf.active(); got != tt.want {
				t.Errorf("active() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAddBatchFlags2 verifies addBatchFlags binds each flag onto the command and
// that parsing populates the backing struct.
func TestAddBatchFlags2(t *testing.T) {
	var bf batchFlags

	cmd := &cobra.Command{Use: "kirk"}
	addBatchFlags(cmd, &bf)

	for _, name := range []string{"repo", "stopped", "stale", "force"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("flag %q not registered", name)
		}
	}

	if err := cmd.Flags().Parse([]string{"--repo", "croft", "--stopped", "--stale", "3d", "--force"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if bf.repo != "croft" || !bf.stopped || bf.stale != "3d" || !bf.force {
		t.Errorf("parsed flags = %+v, want repo=croft stopped stale=3d force", bf)
	}
}

// TestConfirmBatchJSONModeErrors verifies confirmBatch refuses interactive
// confirmation in JSON mode and reports how many sessions would be affected.
func TestConfirmBatchJSONModeErrors(t *testing.T) {
	setDiscardOutForBatch(t, true)

	sessions := []protocol.SessionInfo{
		{Name: "braw"},
		{Name: "canny"},
	}

	confirmed, err := confirmBatch(&cobra.Command{}, "delete", "deleted", sessions)
	if err == nil {
		t.Fatalf("expected error in JSON mode")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}

// TestConfirmBatchNonTerminalErrors covers the no-TTY branch: without an
// interactive terminal (the test environment) confirmBatch cannot prompt and
// must error.
func TestConfirmBatchNonTerminalErrors(t *testing.T) {
	setDiscardOutForBatch(t, false)

	sessions := []protocol.SessionInfo{{Name: "dreich"}}

	confirmed, err := confirmBatch(&cobra.Command{}, "stop", "stopped", sessions)
	if err == nil {
		t.Fatalf("expected error with no TTY")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}

// TestFilterSessions2 covers timestamp edge cases in the stale filter that the
// round-1 table did not: unparseable timestamps and empty timestamps are
// skipped, and CreatedAt is used as a fallback for LastAttachedAt.
func TestFilterSessions2(t *testing.T) {
	old := "2020-01-01T00:00:00Z"

	sessions := []protocol.SessionInfo{
		{ID: "1", Name: "garbage-ts", Status: "running", LastAttachedAt: "not-a-time"},
		{ID: "2", Name: "no-ts", Status: "running", LastAttachedAt: "", CreatedAt: ""},
		{ID: "3", Name: "created-fallback", Status: "running", LastAttachedAt: "", CreatedAt: old},
	}

	got, err := filterSessions(sessions, &batchFlags{stale: "1d"})
	if err != nil {
		t.Fatalf("filterSessions error: %v", err)
	}

	// The garbage timestamp and the no-timestamp session are skipped; only the
	// CreatedAt-fallback session (very old) matches the stale filter.
	if len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("got %+v, want only session 3", got)
	}
}

// TestFilterSessions2InvalidStalePropagates verifies an unparseable stale
// duration surfaces as an error rather than silently matching everything.
func TestFilterSessions2InvalidStalePropagates(t *testing.T) {
	if _, err := filterSessions(nil, &batchFlags{stale: "haar"}); err == nil {
		t.Fatalf("expected error for invalid stale duration")
	}
}

// TestStopNoOpSkip verifies that batch stop treats non-running sessions as
// no-ops (skipped with a note) and only leaves running sessions actionable, so
// `gr stop` on an already-stopped session succeeds instead of erroring (#203).
func TestStopNoOpSkip(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantReason string
		wantSkip   bool
	}{
		{name: "running is actionable", status: "running", wantSkip: false},
		{name: "stopped is a no-op", status: "stopped", wantReason: "not running (stopped)", wantSkip: true},
		{name: "errored is a no-op", status: "errored", wantReason: "not running (errored)", wantSkip: true},
		{name: "empty status is a no-op", status: "", wantReason: "not running", wantSkip: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, skip := stopNoOpSkip(protocol.SessionInfo{Name: "braw", Status: tt.status})
			if skip != tt.wantSkip {
				t.Fatalf("stopNoOpSkip(status=%q) skip = %v, want %v", tt.status, skip, tt.wantSkip)
			}

			if reason != tt.wantReason {
				t.Errorf("stopNoOpSkip(status=%q) reason = %q, want %q", tt.status, reason, tt.wantReason)
			}
		})
	}
}

// TestPartitionNoOp covers the split of matched sessions into actionable and
// skipped no-ops, mirroring the batch-stop scenarios from #203: an
// already-stopped session is skipped, `--stopped` skips all matches, and a
// mixed repo filter stops only the running sessions.
func TestPartitionNoOp(t *testing.T) {
	running := protocol.SessionInfo{ID: "1", Name: "running-croft", Status: "running"}
	stopped := protocol.SessionInfo{ID: "2", Name: "stopped-croft", Status: "stopped"}
	errored := protocol.SessionInfo{ID: "3", Name: "errored-croft", Status: "errored"}

	t.Run("nil noOp leaves everything actionable", func(t *testing.T) {
		matched := []protocol.SessionInfo{running, stopped}

		actionable, skips := partitionNoOp(matched, nil)
		if len(actionable) != 2 || len(skips) != 0 {
			t.Fatalf("nil noOp: actionable=%d skips=%d, want 2/0", len(actionable), len(skips))
		}
	})

	t.Run("already-stopped session is skipped", func(t *testing.T) {
		actionable, skips := partitionNoOp([]protocol.SessionInfo{stopped}, stopNoOpSkip)
		if len(actionable) != 0 {
			t.Fatalf("actionable = %d, want 0", len(actionable))
		}

		if len(skips) != 1 || skips[0] != "stopped-croft: not running (stopped)" {
			t.Fatalf("skips = %v, want one stopped-croft note", skips)
		}
	})

	t.Run("--stopped skips all matches", func(t *testing.T) {
		// filterSessions with --stopped yields only stopped/errored sessions;
		// every one of them is a no-op for stop.
		matched := []protocol.SessionInfo{stopped, errored}

		actionable, skips := partitionNoOp(matched, stopNoOpSkip)
		if len(actionable) != 0 {
			t.Fatalf("actionable = %d, want 0", len(actionable))
		}

		if len(skips) != 2 {
			t.Fatalf("skips = %v, want 2", skips)
		}
	})

	t.Run("mixed states leave only running actionable", func(t *testing.T) {
		matched := []protocol.SessionInfo{running, stopped, errored}

		actionable, skips := partitionNoOp(matched, stopNoOpSkip)
		if len(actionable) != 1 || actionable[0].ID != "1" {
			t.Fatalf("actionable = %+v, want only the running session", actionable)
		}

		if len(skips) != 2 {
			t.Fatalf("skips = %v, want 2", skips)
		}
	})
}

// TestParseStaleDuration2 adds combined day+hour forms not covered by round 1.
func TestParseStaleDuration2(t *testing.T) {
	d, err := parseStaleDuration("3d1h")
	if err != nil {
		t.Fatalf("parseStaleDuration error: %v", err)
	}

	if d.Hours() != 73 {
		t.Errorf("parseStaleDuration(3d1h) = %v hours, want 73", d.Hours())
	}
}

// errorEnvelope builds a daemon "error" response envelope carrying msg.
func errorEnvelope(t *testing.T, msg string) protocol.Envelope {
	t.Helper()

	raw, err := protocol.EncodeControl("error", protocol.ErrorMsg{Message: msg})
	if err != nil {
		t.Fatalf("encode error envelope: %v", err)
	}

	env, err := protocol.DecodeControl(raw)
	if err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}

	return env
}

// okEnvelope builds a successful (non-error) response envelope.
func okEnvelope() protocol.Envelope {
	return protocol.Envelope{Type: "ok"}
}

// fakeBatchConn is a scripted batchConn: each ReadControlResponse call returns
// the next queued response (or read error). It records the control types sent
// so tests can assert every non-skipped session was contacted.
type fakeBatchConn struct {
	responses []protocol.Envelope
	readErrs  []error // parallel to responses; non-nil means ReadControlResponse fails
	sendErr   error   // if set, SendControl fails on the first send

	sent      []string
	readIndex int
}

func (f *fakeBatchConn) SendControl(msgType string, _ any) error {
	if f.sendErr != nil {
		return f.sendErr
	}

	f.sent = append(f.sent, msgType)

	return nil
}

func (f *fakeBatchConn) ReadControlResponse() (protocol.Envelope, error) {
	i := f.readIndex
	f.readIndex++

	if i < len(f.readErrs) && f.readErrs[i] != nil {
		return protocol.Envelope{}, f.readErrs[i]
	}

	return f.responses[i], nil
}

// TestExecuteBatchContinuesPastFailure is the core regression test for #201:
// when a session in the middle fails, executeBatch must still process the
// sessions after it and report both the successes and the failure, rather than
// aborting on the first error.
func TestExecuteBatchContinuesPastFailure(t *testing.T) {
	matched := []protocol.SessionInfo{
		{ID: "1", Name: "braw"},
		{ID: "2", Name: "canny"},
		{ID: "3", Name: "dreich"}, // this one fails
		{ID: "4", Name: "bonnie"},
		{ID: "5", Name: "kirk"},
	}

	conn := &fakeBatchConn{
		responses: []protocol.Envelope{
			okEnvelope(),
			okEnvelope(),
			errorEnvelope(t, "worktree busy"),
			okEnvelope(),
			okEnvelope(),
		},
	}

	res, transportErr := executeBatch(conn, matched, "delete", func(id string) any { return id })
	if transportErr != nil {
		t.Fatalf("unexpected transport error: %v", transportErr)
	}

	wantSucceeded := []string{"braw", "canny", "bonnie", "kirk"}
	if strings.Join(res.succeeded, ",") != strings.Join(wantSucceeded, ",") {
		t.Errorf("succeeded = %v, want %v", res.succeeded, wantSucceeded)
	}

	if len(res.failed) != 1 || res.failed[0].name != "dreich" || res.failed[0].msg != "worktree busy" {
		t.Fatalf("failed = %+v, want one failure for dreich with message %q", res.failed, "worktree busy")
	}

	// All five sessions must have been contacted — the failure at #3 must not
	// have short-circuited #4 and #5.
	if len(conn.sent) != 5 {
		t.Errorf("sent %d control messages, want 5 (failure should not abort the loop)", len(conn.sent))
	}
}

// TestExecuteBatchSkipsStarred verifies starred sessions are skipped without
// contacting the daemon and reported separately from successes.
func TestExecuteBatchSkipsStarred(t *testing.T) {
	matched := []protocol.SessionInfo{
		{ID: "1", Name: "braw"},
		{ID: "2", Name: "thrawn", Starred: true},
		{ID: "3", Name: "canny"},
	}

	conn := &fakeBatchConn{responses: []protocol.Envelope{okEnvelope(), okEnvelope()}}

	res, transportErr := executeBatch(conn, matched, "stop", func(id string) any { return id })
	if transportErr != nil {
		t.Fatalf("unexpected transport error: %v", transportErr)
	}

	if strings.Join(res.succeeded, ",") != "braw,canny" {
		t.Errorf("succeeded = %v, want [braw canny]", res.succeeded)
	}

	if len(res.skipped) != 1 || res.skipped[0] != "thrawn" {
		t.Errorf("skipped = %v, want [thrawn]", res.skipped)
	}

	if len(res.failed) != 0 {
		t.Errorf("failed = %v, want none", res.failed)
	}

	// Only the two non-starred sessions were contacted.
	if len(conn.sent) != 2 {
		t.Errorf("sent %d control messages, want 2 (starred must not be sent)", len(conn.sent))
	}
}

// TestExecuteBatchAllFail verifies every session failing is recorded, with no
// successes, and the loop still runs to completion.
func TestExecuteBatchAllFail(t *testing.T) {
	matched := []protocol.SessionInfo{
		{ID: "1", Name: "dreich"},
		{ID: "2", Name: "fash"},
	}

	conn := &fakeBatchConn{
		responses: []protocol.Envelope{
			errorEnvelope(t, "boom"),
			errorEnvelope(t, "scunner"),
		},
	}

	res, transportErr := executeBatch(conn, matched, "delete", func(id string) any { return id })
	if transportErr != nil {
		t.Fatalf("unexpected transport error: %v", transportErr)
	}

	if len(res.succeeded) != 0 {
		t.Errorf("succeeded = %v, want none", res.succeeded)
	}

	if len(res.failed) != 2 {
		t.Fatalf("failed = %+v, want 2 failures", res.failed)
	}
}

// TestExecuteBatchTransportErrorAborts verifies a read (transport) error stops
// the loop and is returned separately, while the results gathered before it
// are preserved so the caller can still report them.
func TestExecuteBatchTransportErrorAborts(t *testing.T) {
	matched := []protocol.SessionInfo{
		{ID: "1", Name: "braw"},
		{ID: "2", Name: "dreich"}, // read fails here
		{ID: "3", Name: "canny"},
	}

	conn := &fakeBatchConn{
		responses: []protocol.Envelope{okEnvelope(), {}, okEnvelope()},
		readErrs:  []error{nil, errors.New("connection reset"), nil},
	}

	res, transportErr := executeBatch(conn, matched, "stop", func(id string) any { return id })
	if transportErr == nil {
		t.Fatalf("expected transport error, got nil")
	}

	// The first session's success is preserved.
	if strings.Join(res.succeeded, ",") != "braw" {
		t.Errorf("succeeded = %v, want [braw]", res.succeeded)
	}

	// The loop aborted at the second session, so the third was never contacted.
	if len(conn.sent) != 2 {
		t.Errorf("sent %d control messages, want 2 (transport error must abort)", len(conn.sent))
	}
}

// TestExecuteBatchSendErrorAborts verifies a SendControl (transport) failure is
// returned as a transport error rather than silently swallowed.
func TestExecuteBatchSendErrorAborts(t *testing.T) {
	matched := []protocol.SessionInfo{{ID: "1", Name: "braw"}}

	conn := &fakeBatchConn{sendErr: errors.New("broken pipe")}

	_, transportErr := executeBatch(conn, matched, "delete", func(id string) any { return id })
	if transportErr == nil {
		t.Fatalf("expected transport error from failed send, got nil")
	}
}

// TestPrintBatchSummary verifies the summary reports the success count and
// lists both skipped-starred and failed sessions (with the daemon's error).
func TestPrintBatchSummary(t *testing.T) {
	var buf bytes.Buffer

	orig := out

	t.Cleanup(func() { out = orig })

	out = output.NewWithWriter(false, &buf)

	res := batchResults{
		succeeded: []string{"braw", "canny"},
		failed:    []batchFailure{{name: "dreich", msg: "worktree busy"}},
		skipped:   []string{"thrawn"},
		noOps:     []string{"bide: not running (stopped)"},
	}

	printBatchSummary("deleted", "deleting", res)

	got := buf.String()

	for _, want := range []string{
		"Deleted 2 sessions",
		"Skipped starred session: thrawn",
		"Skipped bide: not running (stopped)",
		"Failed deleting dreich: worktree busy",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\nfull output:\n%s", want, got)
		}
	}
}
