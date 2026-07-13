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
	// record): keep the single occurrence with the greater total and flag it.
	lines := []string{
		claudeAsst("a1", "msg_haar", 2, 10, 0, 0),    // partial (total 12)
		claudeAsst("a2", "msg_haar", 10, 400, 0, 50), // full (total 460, wins)
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Input != 10 || u.Output != 400 || u.CacheRead != 50 {
		t.Errorf("usage = %+v, want the greater-total occurrence (10/400/0/50)", u)
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

func TestClaudeUsageConflictPicksGreaterTotalNotFieldMax(t *testing.T) {
	// Crossing fields: (input=100,cache_read=0) and (input=0,cache_read=100),
	// each total 100. A per-field max would synthesize 200 (double-count); the
	// reader must keep ONE occurrence (total 100) and flag it.
	lines := []string{
		claudeAsst("a1", "msg_cross", 100, 0, 0, 0),
		claudeAsst("a2", "msg_cross", 0, 0, 0, 100),
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Total() != 100 {
		t.Errorf("Total = %d, want 100 (one occurrence, not a synthesized 200)", u.Total())
	}

	if u.Dropped == 0 {
		t.Error("Dropped = 0, want >0 (conflict flagged)")
	}
}

func TestClaudeUsageSkipsNegativeCounter(t *testing.T) {
	lines := []string{
		claudeAsst("a1", "msg_ok", 10, 20, 0, 0),
		claudeAsst("a2", "msg_bad", -5, 3, 0, 0), // corrupt
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Input != 10 || u.Output != 20 {
		t.Errorf("usage = %+v, want only the valid record (10/20)", u)
	}

	if u.Dropped == 0 {
		t.Error("Dropped = 0, want the corrupt record flagged")
	}
}

func TestCodexUsageZeroTotalContradiction(t *testing.T) {
	// An explicit total_tokens:0 that contradicts the fields must degrade, not be
	// trusted as an exact 105-token breakdown.
	line := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		`{"input_tokens":100,"cached_input_tokens":20,"output_tokens":5,"total_tokens":0}}}}`

	u, err := codexReader{}.usage(writeLines(t, []string{line}))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Dropped == 0 {
		t.Error("explicit zero total contradicting fields should be degraded")
	}

	if u.Unclassified != 0 || u.Input != 0 {
		t.Errorf("usage = %+v, want fallback Unclassified 0 (provider total is 0)", u)
	}
}

func TestCodexUsageMissingTotalUsesBreakdown(t *testing.T) {
	// No total_tokens field → not a contradiction; the exclusive breakdown stands.
	line := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		`{"input_tokens":100,"cached_input_tokens":20,"output_tokens":5}}}}`

	u, err := codexReader{}.usage(writeLines(t, []string{line}))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Input != 80 || u.CacheRead != 20 || u.Output != 5 || u.Dropped != 0 {
		t.Errorf("usage = %+v, want input 80 cacheR 20 output 5, not degraded", u)
	}
}

func TestCodexUsageReasoningOverOutputDegrades(t *testing.T) {
	// reasoning_output_tokens must be a subset of output_tokens.
	line := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		`{"input_tokens":100,"cached_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":50,"total_tokens":105}}}}`

	u, err := codexReader{}.usage(writeLines(t, []string{line}))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Dropped == 0 {
		t.Error("reasoning > output should degrade")
	}
}

func TestCodexUsageDegradedReflectsFinalSnapshotOnly(t *testing.T) {
	// An early degraded snapshot followed by a clean final one must NOT leave the
	// final total flagged (Codex doesn't switch shapes mid-session, but the code
	// must derive Degraded from the winning snapshot, not a running sum).
	bad := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		`{"input_tokens":100,"cached_input_tokens":0,"output_tokens":5,"total_tokens":999}}}}`
	good := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		`{"input_tokens":100,"cached_input_tokens":0,"output_tokens":5,"total_tokens":105}}}}`

	u, err := codexReader{}.usage(writeLines(t, []string{bad, good}))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0 (final snapshot is clean)", u.Dropped)
	}

	if u.Total() != 105 {
		t.Errorf("Total = %d, want 105 (final wins)", u.Total())
	}
}

func TestCodexUsageOverflowNotConfidentZero(t *testing.T) {
	// A corrupt near-MaxInt64 record must not wrap to a negative total that the
	// CLI would render as a confident "0".
	line := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		`{"input_tokens":9223372036854775807,"cached_input_tokens":0,"output_tokens":1,` +
		`"total_tokens":-9223372036854775808}}}}`

	u, err := codexReader{}.usage(writeLines(t, []string{line}))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if u.Dropped == 0 {
		t.Error("overflowing record should be degraded, not trusted")
	}

	if u.Total() < 0 {
		t.Errorf("Total = %d, want non-negative (no wrap to a confident zero)", u.Total())
	}
}

func TestAddCheckedAndSat(t *testing.T) {
	maxI := int64(^uint64(0) >> 1)

	if _, ok := addChecked(maxI, 1); ok {
		t.Error("addChecked should report overflow")
	}

	if got := addSat(maxI, 5); got != maxI {
		t.Errorf("addSat overflow = %d, want saturate to MaxInt64", got)
	}

	if s, ok := addChecked(2, 3, 4); !ok || s != 9 {
		t.Errorf("addChecked(2,3,4) = %d,%v want 9,true", s, ok)
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
