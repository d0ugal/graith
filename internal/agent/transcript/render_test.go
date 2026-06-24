package transcript

import (
	"os"
	"strings"
	"testing"
	"time"
)

// bumpModTime sets a file's mtime slightly into the future so "newest wins"
// scans are deterministic regardless of write order.
func bumpModTime(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

func TestRenderChronologicalWithToolAndContext(t *testing.T) {
	c := &Conversation{
		SrcAgent: "claude",
		Turns: []Turn{
			{Role: RoleUser, Text: "fix the bothy"},
			{Role: RoleAssistant, Text: "on it"},
			{Role: RoleTool, Tool: &ToolCall{Name: "Bash", Args: `{"command":"ls"}`, Output: "neeps"}},
			{Role: RoleContext, Text: "developer note"},
		},
	}
	out := c.Render(RenderOptions{})

	if !strings.Contains(out, "migrated to a different agent") {
		t.Error("missing migration header note")
	}
	// Chronological order preserved.
	iUser := strings.Index(out, "## User")
	iAsst := strings.Index(out, "## Assistant")
	iTool := strings.Index(out, "## Tool call: Bash")
	if iUser >= iAsst || iAsst >= iTool {
		t.Errorf("turns out of order: user=%d asst=%d tool=%d", iUser, iAsst, iTool)
	}
	if !strings.Contains(out, "not re-executed") {
		t.Error("tool call not marked historical")
	}
	if !strings.Contains(out, "Context (developer)") {
		t.Error("developer context not rendered as context")
	}
}

func TestRenderElidesOldestOverBudget(t *testing.T) {
	turns := make([]Turn, 50)
	for i := range turns {
		turns[i] = Turn{Role: RoleUser, Text: strings.Repeat("x", 200)}
	}
	turns[len(turns)-1].Text = "MOST_RECENT_braw"
	c := &Conversation{SrcAgent: "codex", Turns: turns}

	out := c.Render(RenderOptions{MaxBytes: 1000})
	if !strings.Contains(out, "earlier turn(s) elided") {
		t.Error("expected elision notice")
	}
	if !strings.Contains(out, "MOST_RECENT_braw") {
		t.Error("most recent turn should always be kept")
	}
}

func TestRenderTruncatesToolOutput(t *testing.T) {
	c := &Conversation{
		SrcAgent: "claude",
		Turns: []Turn{
			{Role: RoleTool, Tool: &ToolCall{Name: "Bash", Output: strings.Repeat("y", 5000)}},
		},
	}
	out := c.Render(RenderOptions{MaxToolOutput: 100})
	if !strings.Contains(out, "truncated, 5000 bytes total") {
		t.Error("expected tool output truncation marker")
	}
}

func TestBuildSeedPrompt(t *testing.T) {
	p := BuildSeedPrompt("claude", "/tmp/x/migrated-context-abc.md")
	if !strings.Contains(p, "claude") || !strings.Contains(p, "/tmp/x/migrated-context-abc.md") {
		t.Errorf("seed prompt missing details: %q", p)
	}
	if !strings.Contains(p, "past context") {
		t.Error("seed prompt should frame content as historical")
	}
}
