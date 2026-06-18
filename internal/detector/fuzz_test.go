package detector

import "testing"

func FuzzDetect(f *testing.F) {
	f.Add("claude", "⠋ Working...\nctrl+c to interrupt\n")
	f.Add("claude", "Do you trust the files in this folder?\n")
	f.Add("claude", "output done\n❯\n")
	f.Add("claude", "thrawn neep text\n")
	f.Add("claude", "")
	f.Add("codex", "codex>\n")
	f.Add("codex", "How can I help you today?\n")
	f.Add("codex", "Continue?\n")
	f.Add("claude", "pondering…\n")
	f.Add("claude", "⠙ thinking\n")
	f.Add("claude", "Yes, allow once\n")
	f.Add("claude", "No, and tell Claude what to do differently\n")
	f.Add("claude", "\x1b[31mred text\x1b[0m\n>\n")
	f.Add("claude", "Thinking... (45s · 1234 tokens)\n")
	f.Add("claude", "│ Do you want to proceed?\n")
	f.Add("claude", "connecting... (23s · 456 tokens)\n")
	f.Add("opencode", ">\n")
	f.Add("agy", "❯ Try something\n")

	f.Fuzz(func(t *testing.T, agent, content string) {
		d := New(agent)

		// None of these should panic regardless of input.
		status := d.Detect(content, -1)
		busy := d.IsBusy(content)
		approval := d.NeedsApproval(content)
		ready := d.IsReady(content)

		// Verify invariants:
		// 1. If busy, NeedsApproval must be false (busy takes priority)
		if busy && approval {
			t.Error("IsBusy and NeedsApproval both true — busy should take priority")
		}

		// 2. If busy, IsReady must be false
		if busy && ready {
			t.Error("IsBusy and IsReady both true — busy should take priority")
		}

		// 3. If NeedsApproval, IsReady must be false
		if approval && ready {
			t.Error("NeedsApproval and IsReady both true — approval should take priority")
		}

		// 4. Detect result must match the individual checks
		switch status {
		case StatusActive:
			if !busy {
				t.Error("Detect returned active but IsBusy is false")
			}
		case StatusApproval:
			if !approval {
				t.Error("Detect returned approval but NeedsApproval is false")
			}
		case StatusReady:
			if !ready {
				t.Error("Detect returned ready but IsReady is false")
			}
		case StatusUnknown:
			if busy || approval || ready {
				t.Error("Detect returned unknown but one of the checks is true")
			}
		}
	})
}

func FuzzStripANSI(f *testing.F) {
	f.Add("braw bonnie glen")
	f.Add("\x1b[31mred\x1b[0m")
	f.Add("\x1b]0;title\x07text")
	f.Add("\x1b[1mbold\x1b[0m and \x1b[32mgreen\x1b[0m")
	f.Add("\x9B31mred\x9B0m")
	f.Add("")
	f.Add("\x1b")
	f.Add("\x1b[")
	f.Add("\x1b]")
	f.Add("\x1b]0;unterminated")
	f.Add(string([]byte{0x9B}))

	f.Fuzz(func(t *testing.T, input string) {
		result := StripANSI(input)
		// Stripping should be idempotent: applying it twice gives the same result.
		result2 := StripANSI(result)
		if result != result2 {
			t.Errorf("StripANSI is not idempotent: %q -> %q -> %q", input, result, result2)
		}
		// Result should be no longer than input.
		if len(result) > len(input) {
			t.Errorf("StripANSI output longer than input: %d > %d", len(result), len(input))
		}
	})
}
