package detector

import "testing"

func TestIsBusy_InterruptIndicators(t *testing.T) {
	d := New("claude")

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"ctrl+c indicator", "some output\n  ctrl+c to interrupt\n", true},
		{"esc indicator", "some output\n  esc to interrupt\n", true},
		{"no indicator", "some output\nhello world\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.IsBusy(tt.content); got != tt.want {
				t.Errorf("IsBusy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsBusy_Spinners(t *testing.T) {
	d := New("claude")

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"braille spinner", "⠋ Working...\n", true},
		{"asterisk spinner", "✳ clauding...\n", true},
		{"no spinner", "Hello world\n", false},
		{"box drawing ignored", "│ some content\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.IsBusy(tt.content); got != tt.want {
				t.Errorf("IsBusy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsBusy_ThinkingWords(t *testing.T) {
	d := New("claude")

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"pondering ellipsis", "pondering…\n", true},
		{"clauding ascii ellipsis", "clauding...\n", true},
		{"spinner with word", "⠙ thinking\n", true},
		{"word alone no ellipsis", "pondering\n", false},
		{"thinking with tokens", "Thinking... (45s · 1234 tokens)\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.IsBusy(tt.content); got != tt.want {
				t.Errorf("IsBusy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNeedsApproval_PermissionPrompts(t *testing.T) {
	d := New("claude")

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"trust prompt", "Do you trust the files in this folder?\n", true},
		{"allow once", "Yes, allow once\n", true},
		{"allow always", "Yes, allow always\n", true},
		{"no prompt", "Hello world\n❯\n", false},
		{"MCP permission", "Allow this MCP server\n", true},
		{"tell claude differently", "No, and tell Claude what to do differently\n", true},
		{"arrow keys nav", "Use arrow keys to navigate\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.NeedsApproval(tt.content); got != tt.want {
				t.Errorf("NeedsApproval() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNeedsApproval_ConfirmPatterns(t *testing.T) {
	d := New("claude")

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"Y/n prompt", "Continue? (Y/n)\n", true},
		{"yes/no prompt", "Proceed? [yes/no]\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.NeedsApproval(tt.content); got != tt.want {
				t.Errorf("NeedsApproval() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNeedsApproval_BusyTakesPriority(t *testing.T) {
	d := New("claude")
	// If both busy indicators and approval prompts are present, busy wins
	content := "ctrl+c to interrupt\nDo you trust the files in this folder?\n"
	if d.NeedsApproval(content) {
		t.Error("NeedsApproval should return false when IsBusy is true")
	}
}

func TestNeedsApproval_CodexContinue(t *testing.T) {
	codex := New("codex")
	claude := New("claude")

	content := "Continue?\n"
	if codex.NeedsApproval(content) {
		t.Error("Codex should not treat 'Continue?' as approval")
	}
	if !claude.NeedsApproval(content) {
		t.Error("Claude should treat 'Continue?' as approval")
	}
}

func TestIsReady_PromptCharacters(t *testing.T) {
	d := New("claude")

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"chevron prompt", "output done\n>\n", true},
		{"unicode chevron", "output done\n❯\n", true},
		{"try suggestion", "output done\n❯ Try something\n", true},
		{"not ready when busy", "⠋ Working\n>\n", false},
		{"not ready when approval", "Do you trust the files in this folder?\n>\n", false},
		{"empty content", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.IsReady(tt.content); got != tt.want {
				t.Errorf("IsReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsReady_Codex(t *testing.T) {
	d := New("codex")

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"codex prompt", "codex>\n", true},
		{"how can I help", "How can I help you today?\n", true},
		{"continue prompt", "Continue?\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.IsReady(tt.content); got != tt.want {
				t.Errorf("IsReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetect(t *testing.T) {
	d := New("claude")

	tests := []struct {
		name    string
		content string
		want    AgentStatus
	}{
		{"busy", "⠋ Working\nctrl+c to interrupt\n", StatusActive},
		{"approval", "Do you trust the files in this folder?\n", StatusApproval},
		{"ready", "output done\n❯\n", StatusReady},
		{"unknown", "some random text\n", StatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.Detect(tt.content); got != tt.want {
				t.Errorf("Detect() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no escapes", "hello world", "hello world"},
		{"CSI color", "\x1b[31mred\x1b[0m", "red"},
		{"OSC title", "\x1b]0;title\x07text", "text"},
		{"mixed", "\x1b[1mbold\x1b[0m and \x1b[32mgreen\x1b[0m", "bold and green"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.input); got != tt.want {
				t.Errorf("StripANSI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLastNonEmptyLines(t *testing.T) {
	content := "line1\n\nline2\n\nline3\n\n"
	lines := lastNonEmptyLines(content, 2)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0] != "line2" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "line2")
	}
	if lines[1] != "line3" {
		t.Errorf("lines[1] = %q, want %q", lines[1], "line3")
	}
}
