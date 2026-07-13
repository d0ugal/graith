package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

// claudeAsst builds an assistant record line with a given message id and usage.
func claudeAsst(uuid, msgID string, in, out, cw, cr int64) string {
	return `{"type":"assistant","uuid":"` + uuid + `","message":{"id":"` + msgID +
		`","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":` +
		i64(in) + `,"output_tokens":` + i64(out) +
		`,"cache_creation_input_tokens":` + i64(cw) +
		`,"cache_read_input_tokens":` + i64(cr) +
		`},"content":[{"type":"text","text":"aye"}]}}`
}

func i64(n int64) string {
	// small helper to avoid importing strconv in fixture builders
	if n == 0 {
		return "0"
	}

	neg := n < 0
	if neg {
		n = -n
	}

	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}

	if neg {
		b = append([]byte{'-'}, b...)
	}

	return string(b)
}

func TestClaudeUsageDeduplicatesByMessageID(t *testing.T) {
	// Claude writes one line per content block, all sharing message.id with an
	// identical usage object. A naive sum would triple-count this response.
	lines := []string{
		`{"type":"user","uuid":"u0","message":{"role":"user","content":"do the thing"}}`,
		claudeAsst("a1", "msg_braw", 10, 100, 500, 200),
		claudeAsst("a2", "msg_braw", 10, 100, 500, 200), // same id, identical usage
		claudeAsst("a3", "msg_braw", 10, 100, 500, 200), // same id again
		claudeAsst("a4", "msg_canny", 5, 50, 0, 300),    // a distinct response
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if !u.Found {
		t.Fatal("Found = false, want true")
	}

	if u.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0 (identical duplicates are not conflicts)", u.Dropped)
	}

	// Only two distinct message ids should be counted.
	want := Usage{Input: 15, Output: 150, CacheCreation: 500, CacheRead: 500, Found: true}
	if u.Input != want.Input || u.Output != want.Output ||
		u.CacheCreation != want.CacheCreation || u.CacheRead != want.CacheRead {
		t.Errorf("usage = %+v, want %+v", u, want)
	}

	if got := u.Total(); got != 1165 {
		t.Errorf("Total = %d, want 1165", got)
	}
}

func TestClaudeUsageConflictingDuplicateKeepsLargerAndFlags(t *testing.T) {
	// Same id, different numbers (e.g. a partial live write followed by the full
	// record): keep the larger per-field value and flag the conflict.
	lines := []string{
		claudeAsst("a1", "msg_haar", 2, 10, 0, 0),    // partial
		claudeAsst("a2", "msg_haar", 10, 400, 0, 50), // full
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Input != 10 || u.Output != 400 || u.CacheRead != 50 {
		t.Errorf("usage = %+v, want per-field max (10/400/0/50)", u)
	}

	if u.Dropped == 0 {
		t.Error("Dropped = 0, want >0 for a conflicting duplicate")
	}
}

func TestClaudeUsageNoIDCountedButFlagged(t *testing.T) {
	// A record without a message id can't be deduped; count it, but flag the
	// total as approximate.
	lines := []string{
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","usage":{"input_tokens":7,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[{"type":"text","text":"x"}]}}`,
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Input != 7 || u.Output != 3 {
		t.Errorf("usage = %+v, want input 7 output 3", u)
	}

	if u.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1 (un-deduplicatable record)", u.Dropped)
	}
}

func TestClaudeUsageSkipsUserAndEmpty(t *testing.T) {
	// User records carry no usage; a transcript with none yields Found=false, NOT
	// an error (unlike Read's ErrNoTurns).
	lines := []string{
		`{"type":"user","uuid":"u0","message":{"role":"user","content":"hello"}}`,
		`{"type":"summary","uuid":"s1"}`,
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Found {
		t.Errorf("Found = true, want false for a usage-less transcript")
	}

	if u.Total() != 0 {
		t.Errorf("Total = %d, want 0", u.Total())
	}
}

func TestClaudeUsageCountsSidechains(t *testing.T) {
	// Sidechain (subagent) records still spent tokens, so they are counted —
	// usage does not follow the active-leaf chain that Read uses.
	lines := []string{
		claudeAsst("a1", "msg_main", 10, 20, 0, 0),
		`{"type":"assistant","uuid":"a2","isSidechain":true,"message":{"id":"msg_side","role":"assistant","usage":{"input_tokens":5,"output_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[{"type":"text","text":"sub"}]}}`,
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Input != 15 || u.Output != 28 {
		t.Errorf("usage = %+v, want input 15 output 28 (sidechain included)", u)
	}
}

func TestClaudeUsageDropsMalformedLine(t *testing.T) {
	lines := []string{
		claudeAsst("a1", "msg_ok", 1, 2, 0, 0),
		`{ this is not json`,
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", u.Dropped)
	}

	if u.Input != 1 || u.Output != 2 {
		t.Errorf("usage = %+v, want the valid record still counted", u)
	}
}

// --- Codex ---

func TestCodexUsageModernExclusiveMapping(t *testing.T) {
	// Real-shape record where cached_input ⊂ input and total == input+output.
	// The exclusive breakdown must equal the provider total, not the inflated sum.
	line := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		`{"input_tokens":825355,"cached_input_tokens":756224,"output_tokens":3517,` +
		`"reasoning_output_tokens":577,"total_tokens":828872}}}}`
	path := writeLines(t, []string{
		`{"type":"session_meta","payload":{"id":"s","cwd":"/x"}}`,
		line,
	})

	u, err := codexReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.CacheRead != 756224 {
		t.Errorf("CacheRead = %d, want 756224", u.CacheRead)
	}

	if u.Input != 825355-756224 {
		t.Errorf("Input = %d, want %d (input - cached)", u.Input, 825355-756224)
	}

	if u.Output != 3517 {
		t.Errorf("Output = %d, want 3517 (reasoning NOT added)", u.Output)
	}

	if got := u.Total(); got != 828872 {
		t.Errorf("Total = %d, want 828872 (matches provider total_tokens)", got)
	}

	if u.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0 for a valid breakdown", u.Dropped)
	}
}

func TestCodexUsageCumulativeLastWins(t *testing.T) {
	// token_count is a running total; the last one wins, not the sum.
	mk := func(total int64) string {
		return `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
			`{"input_tokens":` + i64(total) + `,"cached_input_tokens":0,"output_tokens":0,"total_tokens":` + i64(total) + `}}}}`
	}
	path := writeLines(t, []string{
		`{"type":"session_meta","payload":{"id":"s","cwd":"/x"}}`,
		mk(100),
		mk(250),
		mk(900), // final cumulative value
	})

	u, err := codexReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Total() != 900 {
		t.Errorf("Total = %d, want 900 (last cumulative, not sum)", u.Total())
	}
}

func TestCodexUsageOverlapViolationFallsBackToUnclassified(t *testing.T) {
	// total_tokens disagrees with the exclusive sum → degrade to Unclassified
	// rather than reporting a wrong breakdown.
	line := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		`{"input_tokens":100,"cached_input_tokens":0,"output_tokens":50,"total_tokens":999}}}}`
	path := writeLines(t, []string{line})

	u, err := codexReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Unclassified != 999 || u.Input != 0 || u.Output != 0 {
		t.Errorf("usage = %+v, want Unclassified 999 only", u)
	}

	if u.Dropped == 0 {
		t.Error("Dropped = 0, want >0 for a degraded breakdown")
	}

	if u.Total() != 999 {
		t.Errorf("Total = %d, want 999", u.Total())
	}
}

func TestCodexUsageLegacyTotalIsUnclassified(t *testing.T) {
	// The legacy aggregate has no breakdown, so it must land in Unclassified,
	// never Output (which would make the breakdown a lie).
	path := writeLines(t, []string{
		`{"type":"session_meta","payload":{"id":"s","cwd":"/x"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","total":42}}`,
	})

	u, err := codexReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Unclassified != 42 || u.Output != 0 {
		t.Errorf("usage = %+v, want Unclassified 42, Output 0", u)
	}

	if !u.Found || u.Total() != 42 {
		t.Errorf("Found/Total = %v/%d, want true/42", u.Found, u.Total())
	}
}

func TestCodexUsageNoTokenCountNotFound(t *testing.T) {
	path := writeLines(t, []string{
		`{"type":"session_meta","payload":{"id":"s","cwd":"/x"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
	})

	u, err := codexReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Found || u.Total() != 0 {
		t.Errorf("usage = %+v, want Found=false, Total 0", u)
	}
}

// --- package API ---

func TestUsageFromSumsSources(t *testing.T) {
	a := writeLines(t, []string{claudeAsst("a1", "msg_1", 10, 20, 0, 0)})
	b := writeLines(t, []string{claudeAsst("b1", "msg_2", 1, 2, 0, 0)})

	u, err := UsageFrom(AgentClaude, []Source{{Path: a}, {Path: b}})
	if err != nil {
		t.Fatalf("UsageFrom: %v", err)
	}

	if u.Input != 11 || u.Output != 22 || !u.Found {
		t.Errorf("usage = %+v, want summed input 11 output 22", u)
	}
}

func TestUsageFromUnsupportedAgent(t *testing.T) {
	if _, err := UsageFrom("cursor", []Source{{Path: "/x"}}); err == nil {
		t.Error("expected error for unsupported agent")
	}
}

func TestLocateClaudeUsageRoundTrip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	proj := filepath.Join(root, "projects", "-glen-bothy")
	if err := os.MkdirAll(proj, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(proj, "sess-braw.jsonl"),
		[]byte(claudeAsst("a1", "msg_1", 3, 4, 0, 0)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sources, err := Locate(AgentClaude, "sess-braw", "/glen/bothy")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}

	if len(sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(sources))
	}

	if sources[0].Size == 0 || sources[0].ModTime.IsZero() {
		t.Errorf("source missing stat metadata: %+v", sources[0])
	}

	u, err := UsageFrom(AgentClaude, sources)
	if err != nil {
		t.Fatalf("UsageFrom: %v", err)
	}

	if u.Total() != 7 {
		t.Errorf("Total = %d, want 7", u.Total())
	}
}
