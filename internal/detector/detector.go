package detector

import (
	"strings"
	"time"
)

type AgentStatus string

const (
	StatusActive   AgentStatus = "active"
	StatusApproval AgentStatus = "approval"
	StatusReady    AgentStatus = "ready"
	StatusUnknown  AgentStatus = "unknown"
)

type Detector struct {
	tool string
}

func New(tool string) *Detector {
	return &Detector{tool: strings.ToLower(tool)}
}

// spinnerChars are braille and asterisk spinner characters used by Claude Code.
var spinnerChars = []string{
	"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
	"✳", "✽", "✶", "✢",
}

var thinkingWords = []string{
	"accomplishing", "actioning", "actualizing", "baking", "booping",
	"brewing", "calculating", "cerebrating", "channelling", "churning",
	"clauding", "coalescing", "cogitating", "combobulating", "computing",
	"concocting", "conjuring", "considering", "contemplating", "cooking",
	"crafting", "creating", "crunching", "deciphering", "deliberating",
	"determining", "discombobulating", "divining", "doing", "effecting",
	"elucidating", "enchanting", "envisioning", "finagling", "flibbertigibbeting",
	"forging", "forming", "frolicking", "generating", "germinating",
	"hatching", "herding", "honking", "hustling", "ideating",
	"imagining", "incubating", "inferring", "jiving", "manifesting",
	"marinating", "meandering", "moseying", "mulling", "mustering",
	"musing", "noodling", "percolating", "perusing", "philosophising",
	"pondering", "pontificating", "processing", "puttering", "puzzling",
	"reticulating", "ruminating", "scheming", "schlepping", "shimmying",
	"shucking", "simmering", "smooshing", "spelunking", "spinning",
	"stewing", "sussing", "synthesizing", "thinking", "tinkering",
	"transmuting", "unfurling", "unravelling", "vibing", "wandering",
	"whirring", "wibbling", "wizarding", "working", "wrangling",
	"billowing", "gusting", "metamorphosing", "sublimating", "recombobulating", "sautéing",
}

// IsBusy returns true if the terminal content indicates the agent is actively working.
func (d *Detector) IsBusy(content string) bool {
	lines := lastNonEmptyLines(content, 15)
	recentLower := strings.ToLower(strings.Join(lines, "\n"))

	busyIndicators := []string{
		"ctrl+c to interrupt",
		"esc to interrupt",
	}
	for _, indicator := range busyIndicators {
		if strings.Contains(recentLower, indicator) {
			return true
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 0 && isBoxDrawing([]rune(trimmed)[0]) {
			continue
		}
		for _, spinner := range spinnerChars {
			if strings.Contains(line, spinner) {
				return true
			}
		}
	}

	for _, line := range lines {
		lineLower := strings.ToLower(strings.TrimSpace(line))
		for _, word := range thinkingWords {
			if strings.HasPrefix(lineLower, word+"…") || strings.HasPrefix(lineLower, word+"...") {
				return true
			}
			for _, spinner := range spinnerChars {
				if strings.Contains(line, spinner+" "+word) {
					return true
				}
			}
		}
	}

	if strings.Contains(recentLower, "thinking") && strings.Contains(recentLower, "tokens") {
		return true
	}
	if strings.Contains(recentLower, "connecting") && strings.Contains(recentLower, "tokens") {
		return true
	}

	return false
}

// NeedsApproval returns true if the terminal shows a permission/approval dialog.
func (d *Detector) NeedsApproval(content string) bool {
	if d.IsBusy(content) {
		return false
	}

	lines := lastNonEmptyLines(content, 15)
	recent := strings.Join(lines, "\n")

	permissionPrompts := []string{
		"No, and tell Claude what to do differently",
		"Yes, allow once",
		"Yes, allow always",
		"Allow once",
		"Allow always",
		"Would you like to run the following command?",
		"Press enter to confirm or esc to cancel",
		"│ Do you want",
		"│ Would you like",
		"│ Allow",
		"❯ Yes",
		"❯ No",
		"❯ Allow",
		"Do you trust the files in this folder?",
		"Do you trust the contents of this directory?",
		"Workspace Trust Required",
		"Allow this MCP server",
		"Run this command?",
		"Execute this?",
		"Action Required",
		"Waiting for user confirmation",
		"Allow execution of",
		"Use arrow keys to navigate",
		"Press Enter to select",
		"Allow this action",
		"Do you want to proceed?",
		"Do you want to create",
		"Do you want to make this edit",
	}
	for _, prompt := range permissionPrompts {
		if strings.Contains(recent, prompt) {
			return true
		}
	}

	confirmPatterns := []string{
		"(Y/n)", "[Y/n]", "(y/N)", "[y/N]",
		"(yes/no)", "[yes/no]",
		"Continue?", "Proceed?",
		"Approve this plan?",
		"Execute plan?",
	}
	for _, pattern := range confirmPatterns {
		if d.tool == "codex" && pattern == "Continue?" {
			continue
		}
		if strings.Contains(recent, pattern) {
			return true
		}
	}

	return false
}

// IsReady returns true if the terminal shows an input prompt (agent waiting for input).
func (d *Detector) IsReady(content string) bool {
	if d.IsBusy(content) {
		return false
	}
	if d.NeedsApproval(content) {
		return false
	}

	lines := lastNonEmptyLines(content, 15)
	recentLower := strings.ToLower(strings.Join(lines, "\n"))

	if strings.Contains(recentLower, "how can i help") {
		return true
	}

	checkLines := lines
	if len(checkLines) > 5 {
		checkLines = checkLines[len(checkLines)-5:]
	}
	for _, line := range checkLines {
		clean := strings.TrimSpace(StripANSI(line))
		clean = strings.ReplaceAll(clean, " ", " ")

		if d.tool == "codex" {
			if strings.Contains(clean, "codex>") {
				return true
			}
			if strings.Contains(recentLower, "continue?") {
				return true
			}
		}

		if clean == ">" || clean == "❯" || clean == "> " || clean == "❯ " {
			return true
		}
		if strings.HasPrefix(clean, "❯ Try ") || strings.HasPrefix(clean, "> Try ") {
			return true
		}
	}

	return false
}

// RecentOutputThreshold is the duration within which PTY output implies the
// agent is actively working. Used as a fallback when pattern matching cannot
// determine the state.
const RecentOutputThreshold = 3 * time.Second

// OutputAgeUnknown signals that output timing is not available.
const OutputAgeUnknown = time.Duration(-1)

// Detect returns the detected status based on terminal content and how
// recently the PTY produced output. Pass OutputAgeUnknown if timing is
// not available.
func (d *Detector) Detect(content string, outputAge time.Duration) AgentStatus {
	if d.IsBusy(content) {
		return StatusActive
	}
	if d.NeedsApproval(content) {
		return StatusApproval
	}
	if d.IsReady(content) {
		return StatusReady
	}
	if outputAge >= 0 && outputAge < RecentOutputThreshold {
		return StatusActive
	}
	return StatusUnknown
}

func isBoxDrawing(r rune) bool {
	return r == '│' || r == '├' || r == '└' || r == '─' || r == '┌' ||
		r == '┐' || r == '┘' || r == '┤' || r == '┬' || r == '┴' ||
		r == '┼' || r == '╭' || r == '╰' || r == '╮' || r == '╯'
}

func lastNonEmptyLines(content string, n int) []string {
	lines := strings.Split(content, "\n")
	var result []string
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			result = append([]string{lines[i]}, result...)
		}
	}
	return result
}

// StripANSI removes ANSI escape codes from content.
func StripANSI(content string) string {
	if !strings.Contains(content, "\x1b") && !strings.Contains(content, "\x9B") {
		return content
	}

	var b strings.Builder
	b.Grow(len(content))

	i := 0
	for i < len(content) {
		if content[i] == '\x1b' {
			if i+1 < len(content) && content[i+1] == '[' {
				j := i + 2
				for j < len(content) {
					c := content[j]
					if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
						j++
						break
					}
					j++
				}
				i = j
				continue
			}
			if i+1 < len(content) && content[i+1] == ']' {
				bellPos := strings.Index(content[i:], "\x07")
				stPos := strings.Index(content[i:], "\x1b\\")
				switch {
				case bellPos != -1 && (stPos == -1 || bellPos <= stPos):
					i += bellPos + 1
				case stPos != -1:
					i += stPos + 2
				default:
					i = len(content)
				}
				continue
			}
			if i+1 < len(content) {
				i += 2
				continue
			}
		}
		if content[i] == '\x9B' {
			j := i + 1
			for j < len(content) {
				c := content[j]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
					j++
					break
				}
				j++
			}
			i = j
			continue
		}
		b.WriteByte(content[i])
		i++
	}

	return b.String()
}
