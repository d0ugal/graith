package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// claudeConfigDir returns Claude Code's config root, honouring CLAUDE_CONFIG_DIR.
func claudeConfigDir() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".claude"), nil
}

// locateClaude finds a Claude transcript by session id. The on-disk project
// directory name is a lossy encoding of the cwd (all non-alphanumerics become
// '-'), which is unreliable to reconstruct — graith's own worktrees live under
// dotted ~/.graith paths that mis-encode. The session id is unique and graith
// owns it, so we glob for it across all project directories instead.
func locateClaude(agentSessionID string) (string, error) {
	if agentSessionID == "" {
		return "", fmt.Errorf("claude transcript lookup requires an agent session id")
	}

	root, err := claudeConfigDir()
	if err != nil {
		return "", err
	}

	pattern := filepath.Join(root, "projects", "*", agentSessionID+".jsonl")

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("glob claude transcripts: %w", err)
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no claude transcript found for session %s under %s (looked for %s)", agentSessionID, root, pattern)
	}
	// A session id is unique; if multiple match, the first is fine.
	return matches[0], nil
}

type claudeRecord struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	// ID is the "msg_..." identifier. Claude writes one JSONL line per content
	// block, all sharing this id and carrying an identical usage object, so it
	// is the dedup key for token counting (see usage below).
	ID    string       `json:"id"`
	Model string       `json:"model"` // kept for the cost follow-up
	Usage *claudeUsage `json:"usage"` // nil on user records
}

// claudeUsage mirrors Claude's per-response usage object. The four fields are
// mutually exclusive: input_tokens is the fresh (uncached) input, with cache
// reads/writes counted separately.
type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type claudeBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type claudeReader struct{}

func (claudeReader) read(path string) ([]Turn, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	byUUID := make(map[string]claudeRecord)

	var order []string // uuids of user/assistant records in file order

	dropped := 0

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var rec claudeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			dropped++ // partial trailing line / format drift
			continue
		}

		if rec.UUID != "" {
			byUUID[rec.UUID] = rec
		}

		if rec.Type == "user" || rec.Type == "assistant" {
			order = append(order, rec.UUID)
		}
	}

	if err := sc.Err(); err != nil {
		// A long unterminated final line (live file) is tolerated as a drop.
		dropped++
	}

	leaf := activeLeaf(byUUID, order)
	if leaf == "" {
		return nil, dropped, nil
	}

	chain := walkChain(byUUID, leaf)

	var turns []Turn

	toolIdx := make(map[string]int) // tool_use_id -> index into turns
	for _, rec := range chain {
		appendClaudeTurns(rec, &turns, toolIdx)
	}

	return turns, dropped, nil
}

// usage sums token usage across every assistant record in the file — including
// sidechains and abandoned branches, whose tokens were still spent — rather than
// walking the active chain like read. Claude writes one JSONL line per content
// block, all sharing message.id with an identical usage object, so counting is
// deduplicated by message.id (a naive sum inflates counts ~2-3×).
func (claudeReader) usage(path string) (Usage, error) {
	f, err := os.Open(path)
	if err != nil {
		return Usage{}, err
	}
	defer func() { _ = f.Close() }()

	var u Usage

	// seen records the usage already counted for a message id, so a duplicate
	// with differing values can be reconciled deterministically rather than
	// double-counted.
	seen := make(map[string]claudeUsage)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var rec claudeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			u.Dropped++ // partial trailing line / format drift
			continue
		}

		if rec.Type != "assistant" || len(rec.Message) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(rec.Message, &msg); err != nil {
			u.Dropped++
			continue
		}

		if msg.Usage == nil {
			continue // user-authored assistant record without usage; nothing to count
		}

		countClaudeUsage(&u, seen, msg)
	}

	if err := sc.Err(); err != nil {
		u.Dropped++ // long unterminated final line on a live file
	}

	return u, nil
}

// countClaudeUsage folds one assistant message's usage into u, deduplicating by
// message id. A record with no id can't be deduped, so it is counted but flags
// the total as approximate (Dropped). A duplicate id whose usage differs from
// the first occurrence keeps the larger per-field value (guarding against a
// partial live write) and is flagged.
func countClaudeUsage(u *Usage, seen map[string]claudeUsage, msg claudeMessage) {
	cu := *msg.Usage
	u.Found = true

	if msg.ID == "" {
		addClaudeUsage(u, cu)
		u.Dropped++ // un-deduplicatable: total is approximate

		return
	}

	prev, dup := seen[msg.ID]
	if !dup {
		seen[msg.ID] = cu
		addClaudeUsage(u, cu)

		return
	}

	if cu == prev {
		return // identical repeat of an already-counted response — the common case
	}

	// Same id, different numbers: reconcile to the larger per-field value and
	// flag the conflict rather than trusting either occurrence.
	merged := maxClaudeUsage(prev, cu)
	adjustClaudeUsage(u, prev, merged)
	seen[msg.ID] = merged
	u.Dropped++
}

func addClaudeUsage(u *Usage, cu claudeUsage) {
	u.Input += cu.InputTokens
	u.Output += cu.OutputTokens
	u.CacheCreation += cu.CacheCreationInputTokens
	u.CacheRead += cu.CacheReadInputTokens
}

// adjustClaudeUsage replaces a previously-counted usage with a new one by
// applying the per-field delta.
func adjustClaudeUsage(u *Usage, from, to claudeUsage) {
	u.Input += to.InputTokens - from.InputTokens
	u.Output += to.OutputTokens - from.OutputTokens
	u.CacheCreation += to.CacheCreationInputTokens - from.CacheCreationInputTokens
	u.CacheRead += to.CacheReadInputTokens - from.CacheReadInputTokens
}

func maxClaudeUsage(a, b claudeUsage) claudeUsage {
	return claudeUsage{
		InputTokens:              maxInt64(a.InputTokens, b.InputTokens),
		OutputTokens:             maxInt64(a.OutputTokens, b.OutputTokens),
		CacheCreationInputTokens: maxInt64(a.CacheCreationInputTokens, b.CacheCreationInputTokens),
		CacheReadInputTokens:     maxInt64(a.CacheReadInputTokens, b.CacheReadInputTokens),
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}

	return b
}

// activeLeaf picks the last non-sidechain user/assistant record in file order.
func activeLeaf(byUUID map[string]claudeRecord, order []string) string {
	for i := len(order) - 1; i >= 0; i-- {
		rec, ok := byUUID[order[i]]
		if ok && !rec.IsSidechain {
			return rec.UUID
		}
	}

	return ""
}

// walkChain follows parentUuid from the leaf back to the root, returning the
// records in chronological (root-first) order.
func walkChain(byUUID map[string]claudeRecord, leaf string) []claudeRecord {
	var rev []claudeRecord

	seen := make(map[string]bool)

	cur := leaf
	for cur != "" && !seen[cur] {
		seen[cur] = true

		rec, ok := byUUID[cur]
		if !ok {
			break
		}

		if rec.Type == "user" || rec.Type == "assistant" {
			rev = append(rev, rec)
		}

		cur = rec.ParentUUID
	}
	// reverse
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}

	return rev
}

func appendClaudeTurns(rec claudeRecord, turns *[]Turn, toolIdx map[string]int) {
	var msg claudeMessage
	if err := json.Unmarshal(rec.Message, &msg); err != nil {
		return
	}

	role := RoleUser
	if rec.Type == "assistant" {
		role = RoleAssistant
	}

	// Content may be a plain string or an array of blocks.
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		if strings.TrimSpace(s) != "" {
			*turns = append(*turns, Turn{Role: role, Text: s})
		}

		return
	}

	var blocks []claudeBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return
	}

	var text strings.Builder

	flush := func() {
		if text.Len() > 0 {
			*turns = append(*turns, Turn{Role: role, Text: text.String()})
			text.Reset()
		}
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}

				text.WriteString(b.Text)
			}
		case "thinking", "redacted_thinking":
			// signed/opaque reasoning — not portable, dropped.
		case "tool_use":
			flush()

			*turns = append(*turns, Turn{
				Role: RoleTool,
				Tool: &ToolCall{Name: b.Name, Args: compactJSON(b.Input)},
			})
			if b.ID != "" {
				toolIdx[b.ID] = len(*turns) - 1
			}
		case "tool_result":
			out := blockContentToText(b.Content)
			if idx, ok := toolIdx[b.ToolUseID]; ok {
				(*turns)[idx].Tool.Output = out
				(*turns)[idx].Tool.Failed = b.IsError
			} else if out != "" {
				flush()

				*turns = append(*turns, Turn{Role: RoleTool, Tool: &ToolCall{Name: "(result)", Output: out, Failed: b.IsError}})
			}
		}
	}

	flush()
}

// blockContentToText flattens a tool_result content field, which is either a
// plain string or an array of {type:"text", text:"..."} blocks.
func blockContentToText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var blocks []claudeBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder

		for _, blk := range blocks {
			if blk.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}

				b.WriteString(blk.Text)
			}
		}

		return b.String()
	}

	return string(raw)
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}

	return buf.String()
}
