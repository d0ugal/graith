package transcript

import (
	"bufio"
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

	var best string
	var bestMod int64
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

	var best string
	var bestMod int64
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
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for i := 0; sc.Scan() && i < 5; i++ { // session_meta is at the top
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
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for i := 0; sc.Scan() && i < 5; i++ {
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

type codexReader struct{}

func (codexReader) read(path string) ([]Turn, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	var turns []Turn
	toolIdx := make(map[string]int) // call_id -> index into turns
	dropped := 0

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
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
