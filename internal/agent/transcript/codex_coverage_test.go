package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setMtime forces a file's modification time so mtime-ordered / since-filtered
// scans are deterministic regardless of write order.
func setMtime(t *testing.T, path string, tm time.Time) {
	t.Helper()

	if err := os.Chtimes(path, tm, tm); err != nil {
		t.Fatal(err)
	}
}

func TestLocateCodexByIDCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	// This rollout's cwd does NOT match, but its id does — the id lookup wins.
	writeRollout(t, day, "rollout-1-canny.jsonl", "sess-canny", "/glen/elsewhere")
	// A distractor that matches the cwd but has a different id.
	writeRollout(t, day, "rollout-2-bide.jsonl", "sess-bide", cwd)

	got, err := locateCodex("sess-canny", cwd)
	if err != nil {
		t.Fatalf("locateCodex by id: %v", err)
	}

	if got == "" || !strings.HasSuffix(got, "rollout-1-canny.jsonl") {
		t.Errorf("located %q, want the sess-canny rollout", got)
	}
}

func TestLocateCodexIDMissFallsBackToCwdCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	writeRollout(t, day, "rollout-1-haar.jsonl", "sess-haar", cwd)

	// The requested id is absent, so findCodexRolloutByID returns false and the
	// cwd scan takes over.
	got, err := locateCodex("no-such-id-thrawn", cwd)
	if err != nil {
		t.Fatalf("locateCodex fallback: %v", err)
	}

	if !strings.HasSuffix(got, "rollout-1-haar.jsonl") {
		t.Errorf("located %q, want the cwd-matched rollout", got)
	}
}

func TestFindCodexRolloutByIDNotFoundCov(t *testing.T) {
	day := writeCodexSessions(t)
	writeRollout(t, day, "rollout-1-ken.jsonl", "sess-ken", "/glen/somewhere")

	if _, ok := findCodexRolloutByID(day, "absent-id"); ok {
		t.Error("expected findCodexRolloutByID to report not found")
	}

	got, ok := findCodexRolloutByID(day, "sess-ken")
	if !ok || !strings.HasSuffix(got, "rollout-1-ken.jsonl") {
		t.Errorf("findCodexRolloutByID = %q, %v; want the ken rollout, true", got, ok)
	}
}

func TestLocateCodexSinceCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	since := time.Now()

	stale := writeRollout(t, day, "rollout-1-auld.jsonl", "sess-auld", cwd)
	setMtime(t, stale, since.Add(-time.Hour)) // before since → excluded

	fresh := writeRollout(t, day, "rollout-2-bonnie.jsonl", "sess-bonnie", cwd)
	setMtime(t, fresh, since.Add(time.Hour)) // after since → the winner

	got, ok := LocateCodexSince(cwd, since)
	if !ok {
		t.Fatal("LocateCodexSince returned not found")
	}

	if !strings.HasSuffix(got, "rollout-2-bonnie.jsonl") {
		t.Errorf("located %q, want the fresh rollout", got)
	}
}

func TestLocateCodexSinceNoneCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	old := writeRollout(t, day, "rollout-1-dreich.jsonl", "sess-dreich", cwd)
	setMtime(t, old, time.Now().Add(-2*time.Hour))

	// Everything predates `since`, so nothing qualifies.
	if _, ok := LocateCodexSince(cwd, time.Now()); ok {
		t.Error("expected no rollout at/after since")
	}
}

func TestLocateCodexSinceInExplicitRootCov(t *testing.T) {
	root := t.TempDir()
	// Deliberately do NOT set CODEX_HOME; pass the root explicitly instead.
	day := filepath.Join(root, "sessions", "2026", "07", "09")
	if err := os.MkdirAll(day, 0o750); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	fresh := writeRollout(t, day, "rollout-1-glen.jsonl", "sess-glen", cwd)
	setMtime(t, fresh, time.Now().Add(time.Hour))

	got, ok := LocateCodexSinceIn(root, cwd, time.Now().Add(-time.Hour))
	if !ok || !strings.HasSuffix(got, "rollout-1-glen.jsonl") {
		t.Errorf("LocateCodexSinceIn = %q, %v; want the glen rollout, true", got, ok)
	}
}

func TestCodexSessionIDSinceSingleCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	since := time.Now().Add(-time.Minute)

	// Two rollouts for the same cwd sharing ONE session id → unambiguous.
	a := writeRollout(t, day, "rollout-1-braw.jsonl", "sess-braw", cwd)
	setMtime(t, a, time.Now())
	b := writeRollout(t, day, "rollout-2-braw.jsonl", "sess-braw", cwd)
	setMtime(t, b, time.Now())

	id, ok := CodexSessionIDSince("", cwd, since)
	if !ok || id != "sess-braw" {
		t.Errorf("CodexSessionIDSince = %q, %v; want sess-braw, true", id, ok)
	}
}

func TestCodexSessionIDSinceAmbiguousCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	since := time.Now().Add(-time.Minute)

	a := writeRollout(t, day, "rollout-1-one.jsonl", "sess-one", cwd)
	setMtime(t, a, time.Now())
	b := writeRollout(t, day, "rollout-2-two.jsonl", "sess-two", cwd)
	setMtime(t, b, time.Now())

	// Two DIFFERENT ids in the (since, cwd) window → refuse to guess.
	if id, ok := CodexSessionIDSince("", cwd, since); ok {
		t.Errorf("expected ambiguous refusal, got %q, %v", id, ok)
	}
}

func TestCodexSessionIDSinceNoneCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	// Rollout is for a different cwd, so nothing matches.
	writeRollout(t, day, "rollout-1-fash.jsonl", "sess-fash", "/glen/other")

	if id, ok := CodexSessionIDSince("", cwd, time.Now().Add(-time.Hour)); ok {
		t.Errorf("expected none found, got %q, %v", id, ok)
	}
}

func TestCodexOutputTextCov(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", ``, ""},
		{"plain string", `"neeps and tatties"`, "neeps and tatties"},
		{"object content", `{"content":"from content field"}`, "from content field"},
		{"object output", `{"output":"from output field"}`, "from output field"},
		{"content wins over output", `{"content":"the content","output":"the output"}`, "the content"},
		{"raw fallback", `[1,2,3]`, "[1,2,3]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := codexOutputText([]byte(tc.raw))
			if got != tc.want {
				t.Errorf("codexOutputText(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCodexContentTextCov(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", ``, ""},
		{"plain string", `"a braw string"`, "a braw string"},
		{"parts joined", `[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]`, "line one\nline two"},
		{"parts skip empty", `[{"type":"text","text":""},{"type":"text","text":"kept"}]`, "kept"},
		{"unparseable", `{"not":"an array or string"}`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := codexContentText([]byte(tc.raw))
			if got != tc.want {
				t.Errorf("codexContentText(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestAppendCodexTurnUnpairedOutputCov(t *testing.T) {
	var turns []Turn

	toolIdx := make(map[string]int)

	// A function_call_output with no matching call_id and non-empty output
	// becomes a standalone "(result)" tool turn.
	appendCodexTurn(codexResponseItem{
		Type:   "function_call_output",
		CallID: "orphan",
		Output: []byte(`"orphaned neeps"`),
	}, &turns, toolIdx)

	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}

	if turns[0].Tool == nil || turns[0].Tool.Name != "(result)" || turns[0].Tool.Output != "orphaned neeps" {
		t.Errorf("unexpected orphan tool turn: %+v", turns[0].Tool)
	}

	// An orphan output with empty content produces no turn at all.
	appendCodexTurn(codexResponseItem{
		Type:   "function_call_output",
		CallID: "orphan-empty",
		Output: []byte(`""`),
	}, &turns, toolIdx)

	if len(turns) != 1 {
		t.Errorf("empty orphan output should add no turn; got %d turns", len(turns))
	}
}

func TestAppendCodexTurnCustomToolAliasesCov(t *testing.T) {
	var turns []Turn

	toolIdx := make(map[string]int)

	// custom_tool_call / custom_tool_call_output share the function_call path.
	appendCodexTurn(codexResponseItem{
		Type:      "custom_tool_call",
		Name:      "browser",
		Arguments: `{"url":"x"}`,
		CallID:    "ct1",
	}, &turns, toolIdx)
	appendCodexTurn(codexResponseItem{
		Type:   "custom_tool_call_output",
		CallID: "ct1",
		Output: []byte(`"rendered"`),
	}, &turns, toolIdx)

	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}

	if turns[0].Tool == nil || turns[0].Tool.Name != "browser" || turns[0].Tool.Output != "rendered" {
		t.Errorf("custom tool call not paired: %+v", turns[0].Tool)
	}
}

func TestCodexSessionIDSinceBoundaryAndMissingIDCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	since := time.Now()

	// mtime exactly at `since` must be included (documented "at or after").
	boundary := writeRollout(t, day, "rollout-1-brig.jsonl", "sess-brig", cwd)
	setMtime(t, boundary, since)

	id, ok := CodexSessionIDSince("", cwd, since)
	if !ok || id != "sess-brig" {
		t.Errorf("boundary mtime = %q, %v; want sess-brig, true (equality included)", id, ok)
	}
}

func TestCodexSessionIDSinceEmptyIDCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	// The only matching rollout has an empty session id → not usable.
	fresh := writeRollout(t, day, "rollout-1-skelf.jsonl", "", cwd)
	setMtime(t, fresh, time.Now())

	if id, ok := CodexSessionIDSince("", cwd, time.Now().Add(-time.Hour)); ok {
		t.Errorf("expected no id for empty-id rollout, got %q, %v", id, ok)
	}
}

func TestLocateCodexSkipsCompressedCov(t *testing.T) {
	day := writeCodexSessions(t)
	cwd := t.TempDir()

	// A compressed rollout matching the cwd must be ignored (only live .jsonl
	// files are valid migration sources).
	zst := writeRollout(t, day, "rollout-1-frost.jsonl", "sess-frost", cwd)
	if err := os.Rename(zst, zst+".zst"); err != nil {
		t.Fatal(err)
	}

	if _, err := locateCodex("", cwd); err == nil {
		t.Error("expected no rollout found when only a .jsonl.zst matches")
	}

	// Adding a real .jsonl for the same cwd is now locatable.
	writeRollout(t, day, "rollout-2-frost.jsonl", "sess-frost", cwd)

	if _, err := locateCodex("", cwd); err != nil {
		t.Errorf("expected the uncompressed rollout to be located: %v", err)
	}
}
