package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// codexHome returns Codex's config root, honouring CODEX_HOME.
func codexHome() (string, error) {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".codex"), nil
}

// canonPath resolves symlinks and cleans a path for comparison.
func canonPath(p string) string {
	if p == "" {
		return ""
	}

	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}

	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}

	return filepath.Clean(p)
}

// locateCodex finds the most recent Codex rollout whose session_meta.cwd
// matches the given worktree. Codex assigns its own session id and does not let
// graith set one, so until graith captures the id this is a cwd scan. Cold
// rollouts may be zstd-compressed (.jsonl.zst); those are skipped (a live
// migration source is always an uncompressed .jsonl).
func locateCodex(agentSessionID, worktreePath string) (string, error) {
	root, err := codexHome()
	if err != nil {
		return "", err
	}

	sessionsDir := filepath.Join(root, "sessions")

	// Prefer a deterministic lookup by the captured session id; fall back to the
	// cwd scan only when graith has no id (e.g. capture timed out).
	if agentSessionID != "" {
		if p, ok := findCodexRolloutByID(sessionsDir, agentSessionID); ok {
			return p, nil
		}
	}

	want := canonPath(worktreePath)

	var (
		best    string
		bestMod int64
	)

	err = filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees
		}

		if d.IsDir() {
			return nil
		}

		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}

		cwd, ok := codexRolloutCwd(path)
		if !ok || canonPath(cwd) != want {
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}

		if mod := info.ModTime().UnixNano(); mod >= bestMod {
			bestMod = mod
			best = path
		}

		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scan codex sessions under %s: %w", sessionsDir, err)
	}

	if best == "" {
		return "", fmt.Errorf("no codex rollout found for cwd %s under %s", worktreePath, sessionsDir)
	}

	return best, nil
}

// findCodexRolloutByID scans for a rollout whose session_meta.id matches.
func findCodexRolloutByID(sessionsDir, id string) (string, bool) {
	var found string

	_ = filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}

		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}

		if rid, ok := CodexRolloutID(path); ok && rid == id {
			found = path
		}

		return nil
	})

	if found == "" {
		return "", false
	}

	return found, true
}

// LocateCodexSince returns the newest Codex rollout for a cwd whose mtime is at
// or after `since`. Used for post-start session-id capture: filtering by start
// time avoids picking a stale rollout from a prior session in the same cwd
// (a real hazard for in-place sessions and codex→codex migrations).
func LocateCodexSince(worktreePath string, since time.Time) (string, bool) {
	return LocateCodexSinceIn("", worktreePath, since)
}

// LocateCodexSinceIn is LocateCodexSince scoped to an explicit Codex state root
// (CODEX_HOME). Pass "" to use the daemon's default root. This matters because
// the daemon-side scrape runs in the daemon process, but CODEX_HOME can be set
// per-session via the agent's launch env — reading the daemon's os.Getenv would
// scan the wrong directory and silently miss the rollout.
func LocateCodexSinceIn(root, worktreePath string, since time.Time) (string, bool) {
	if root == "" {
		var err error

		root, err = codexHome()
		if err != nil {
			return "", false
		}
	}

	sessionsDir := filepath.Join(root, "sessions")
	want := canonPath(worktreePath)

	var (
		best    string
		bestMod int64
	)

	_ = filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil || info.ModTime().Before(since) {
			return nil
		}

		cwd, ok := codexRolloutCwd(path)
		if !ok || canonPath(cwd) != want {
			return nil
		}

		if mod := info.ModTime().UnixNano(); mod >= bestMod {
			bestMod = mod
			best = path
		}

		return nil
	})

	if best == "" {
		return "", false
	}

	return best, true
}

// CodexSessionIDSince returns the native session id of the Codex rollout for a
// cwd created at/after `since`. Unlike LocateCodexSinceIn (newest-by-mtime), it
// refuses to guess when the (since, cwd) window contains rollouts with two or
// more DIFFERENT session ids — the concurrent mirror / in-place case —
// returning ("", false) so the caller falls back to a non-pinned resume rather
// than cross-assigning another session's conversation. Pass root "" for the
// daemon default.
func CodexSessionIDSince(root, worktreePath string, since time.Time) (string, bool) {
	if root == "" {
		var err error

		root, err = codexHome()
		if err != nil {
			return "", false
		}
	}

	sessionsDir := filepath.Join(root, "sessions")
	want := canonPath(worktreePath)

	seen := make(map[string]struct{})

	var found string

	_ = filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil || info.ModTime().Before(since) {
			return nil
		}

		cwd, ok := codexRolloutCwd(path)
		if !ok || canonPath(cwd) != want {
			return nil
		}

		id, ok := CodexRolloutID(path)
		if !ok || id == "" {
			return nil
		}

		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			found = id
		}

		return nil
	})

	if len(seen) != 1 {
		return "", false // 0 = none yet, 2+ = ambiguous (concurrent same-cwd)
	}

	return found, true
}

type codexLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type codexSessionMeta struct {
	ID  string `json:"id"`
	CWD string `json:"cwd"`
}

type codexResponseItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
}

type codexContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// codexRolloutCwd reads just the session_meta line to extract cwd.
func codexRolloutCwd(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	sc := newBoundedLineScanner(f, maxMetadataLineBytes())

	for i := 0; i < 5 && sc.Scan(); i++ { // session_meta is at the top
		if sc.Oversized() {
			continue
		}

		var line codexLine
		if err := json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &line); err != nil {
			continue
		}

		if line.Type == "session_meta" {
			var m codexSessionMeta
			if err := json.Unmarshal(line.Payload, &m); err == nil {
				return m.CWD, true
			}
		}
	}

	return "", false
}

// CodexRolloutID reads a rollout's session_meta id. Exported for the daemon's
// post-start id capture.
func CodexRolloutID(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	sc := newBoundedLineScanner(f, maxMetadataLineBytes())

	for i := 0; i < 5 && sc.Scan(); i++ {
		if sc.Oversized() {
			continue
		}

		var line codexLine
		if err := json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &line); err != nil {
			continue
		}

		if line.Type == "session_meta" {
			var m codexSessionMeta
			if err := json.Unmarshal(line.Payload, &m); err == nil && m.ID != "" {
				return m.ID, true
			}
		}
	}

	return "", false
}

// codexTokenCountPayload is the payload of a token_count record, in either the
// legacy shape (a bare `total` int) or the modern shape (`info.total_token_usage`).
type codexTokenCountPayload struct {
	Type  string          `json:"type"` // "token_count" when wrapped in event_msg
	Total *int64          `json:"total"`
	Info  *codexTokenInfo `json:"info"`
}

type codexTokenInfo struct {
	TotalTokenUsage *codexTokenUsage `json:"total_token_usage"`
}

// codexTokenUsage is Codex's cumulative usage object. Its fields OVERLAP:
// cached_input_tokens is a subset of input_tokens, reasoning_output_tokens a
// subset of output_tokens, and total_tokens == input_tokens + output_tokens. A
// naive additive mapping double-counts, so usage derives an exclusive breakdown
// and validates it against total_tokens.
type codexTokenUsage struct {
	InputTokens           int64  `json:"input_tokens"`
	CachedInputTokens     int64  `json:"cached_input_tokens"`
	OutputTokens          int64  `json:"output_tokens"`
	ReasoningOutputTokens int64  `json:"reasoning_output_tokens"`
	TotalTokens           *int64 `json:"total_tokens"` // pointer: distinguish missing from explicit 0
}

type codexReader struct{}

// usage returns the token usage for a Codex rollout. token_count records are
// CUMULATIVE running totals, so the last valid one wins (not a sum). Both the
// legacy `total` int and the modern `info.total_token_usage` object are handled.
func (codexReader) usage(path string) (Usage, error) {
	f, err := os.Open(path)
	if err != nil {
		return Usage{}, err
	}
	defer func() { _ = f.Close() }()

	var (
		u      Usage
		last   Usage // most recent cumulative token_count seen
		havany bool
	)

	sc := newBoundedLineScanner(f, maxLineBytes())
	dropped := 0

	for sc.Scan() {
		if sc.Oversized() {
			dropped++
			continue
		}

		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}

		var line codexLine
		if err := json.Unmarshal(raw, &line); err != nil {
			continue // non-token lines are irrelevant here; don't count as dropped
		}

		if line.Type != "event_msg" && line.Type != "token_count" {
			continue
		}

		tc, ok := codexUsageFromPayload(line.Payload)
		if !ok {
			continue // not a token_count payload
		}

		last = tc
		havany = true
	}

	if err := sc.Err(); err != nil {
		dropped++
	}

	if havany {
		// Degradation reflects the FINAL (winning) snapshot's own validation
		// plus any scan error — not a running sum over superseded snapshots,
		// which would falsely flag a clean final total.
		last.Dropped += dropped
		last.Found = true

		return last, nil
	}

	u.Dropped = dropped

	return u, nil
}

// codexUsageFromPayload parses a token_count payload into an exclusive Usage.
// A degraded breakdown carries Dropped=1 in the returned Usage. The second
// return reports whether the payload was a token_count at all.
func codexUsageFromPayload(raw json.RawMessage) (Usage, bool) {
	if len(raw) == 0 {
		return Usage{}, false
	}

	var p codexTokenCountPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Usage{}, false
	}

	// A wrapped event_msg payload announces its own type; an unwrapped
	// token_count record carries total/info directly.
	if p.Type != "" && p.Type != "token_count" {
		return Usage{}, false
	}

	if p.Info != nil && p.Info.TotalTokenUsage != nil {
		return codexUsageFromTotals(*p.Info.TotalTokenUsage), true
	}

	if p.Total != nil {
		// Legacy aggregate with no breakdown → Unclassified, never Output. A
		// negative aggregate is corrupt: clamp to 0 and flag degraded.
		if *p.Total < 0 {
			return Usage{Found: true, Dropped: 1}, true
		}

		return Usage{Unclassified: *p.Total, Found: true}, true
	}

	return Usage{}, false
}

// codexUsageFromTotals maps Codex's overlapping cumulative fields to an
// exclusive breakdown and validates it. reasoning_output_tokens is deliberately
// NOT added to Output (it is a subset of output_tokens). On any violation —
// negative field, cached not a subset of input, reasoning not a subset of
// output, or (when a provider total is present, incl. an explicit 0) a sum that
// disagrees with total_tokens — it falls back to an unclassified provider total
// and flags Dropped, so drift degrades to an honest total rather than a wrong
// breakdown.
func codexUsageFromTotals(t codexTokenUsage) Usage {
	cacheRead := t.CachedInputTokens
	input := t.InputTokens - t.CachedInputTokens
	output := t.OutputTokens

	valid := t.InputTokens >= 0 && t.CachedInputTokens >= 0 && t.OutputTokens >= 0 &&
		t.ReasoningOutputTokens >= 0 &&
		input >= 0 && // cached_input ⊆ input
		t.ReasoningOutputTokens <= t.OutputTokens // reasoning_output ⊆ output

	if valid && t.TotalTokens != nil {
		if sum, ok := addChecked(input, cacheRead, output); !ok || sum != *t.TotalTokens {
			valid = false
		}
	}

	if !valid {
		return Usage{Unclassified: codexFallbackTotal(t), Found: true, Dropped: 1}
	}

	return Usage{Input: input, CacheRead: cacheRead, Output: output, Found: true}
}

// codexFallbackTotal is the honest total to report when the breakdown fails
// validation: the provider total when present and non-negative, else the
// (checked, non-negative) sum of raw input+output, else 0.
func codexFallbackTotal(t codexTokenUsage) int64 {
	if t.TotalTokens != nil && *t.TotalTokens >= 0 {
		return *t.TotalTokens
	}

	sum, ok := addChecked(maxInt64(t.InputTokens, 0), maxInt64(t.OutputTokens, 0))
	if !ok || sum < 0 {
		return 0
	}

	return sum
}

func (codexReader) read(path string) ([]Turn, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	var turns []Turn

	toolIdx := make(map[string]int) // call_id -> index into turns
	dropped := 0

	sc := newBoundedLineScanner(f, maxLineBytes())

	for sc.Scan() {
		if sc.Oversized() {
			dropped++
			continue
		}

		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}

		var line codexLine
		if err := json.Unmarshal(raw, &line); err != nil {
			dropped++
			continue
		}

		if line.Type != "response_item" {
			// session_meta, event_msg, token_count, turn_context, compacted,
			// reasoning — not conversation content.
			continue
		}

		var item codexResponseItem
		if err := json.Unmarshal(line.Payload, &item); err != nil {
			dropped++
			continue
		}

		appendCodexTurn(item, &turns, toolIdx)
	}

	if err := sc.Err(); err != nil {
		dropped++
	}

	return turns, dropped, nil
}

func appendCodexTurn(item codexResponseItem, turns *[]Turn, toolIdx map[string]int) {
	switch item.Type {
	case "message":
		text := codexContentText(item.Content)
		if strings.TrimSpace(text) == "" {
			return
		}

		role := RoleUser

		switch item.Role {
		case "assistant":
			role = RoleAssistant
		case "developer":
			role = RoleContext
		case "user":
			// already RoleUser
		default:
			role = RoleContext
		}

		*turns = append(*turns, Turn{Role: role, Text: text})
	case "function_call", "custom_tool_call":
		*turns = append(*turns, Turn{
			Role: RoleTool,
			Tool: &ToolCall{Name: item.Name, Args: item.Arguments},
		})
		if item.CallID != "" {
			toolIdx[item.CallID] = len(*turns) - 1
		}
	case "function_call_output", "custom_tool_call_output":
		out := codexOutputText(item.Output)
		if idx, ok := toolIdx[item.CallID]; ok {
			(*turns)[idx].Tool.Output = out
		} else if out != "" {
			*turns = append(*turns, Turn{Role: RoleTool, Tool: &ToolCall{Name: "(result)", Output: out}})
		}
	}
}

// codexContentText flattens a message content array of {type, text} parts.
func codexContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var parts []codexContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}

	var b strings.Builder

	for _, p := range parts {
		if p.Text == "" {
			continue
		}

		if b.Len() > 0 {
			b.WriteString("\n")
		}

		b.WriteString(p.Text)
	}

	return b.String()
}

// codexOutputText flattens a function_call_output payload, which may be a
// string or an object with a "content" field.
func codexOutputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var obj struct {
		Content string `json:"content"`
		Output  string `json:"output"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Content != "" {
			return obj.Content
		}

		return obj.Output
	}

	return string(raw)
}
