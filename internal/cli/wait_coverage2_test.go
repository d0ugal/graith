package cli

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

// withJSONCov2 flips the package-global jsonOutput flag (which captureStdout
// reads when rebinding `out`) and restores it afterwards.
func withJSONCov2(t *testing.T, on bool, fn func()) {
	t.Helper()

	prev := jsonOutput
	jsonOutput = on

	defer func() { jsonOutput = prev }()

	fn()
}

func TestReportWaitMatchedCov2MatchedLine(t *testing.T) {
	withJSONCov2(t, false, func() {
		got := captureStdout(t, func() {
			reportWaitMatched(protocol.WaitMatchedMsg{MatchedLine: "tests passed"})
		})

		if !strings.Contains(got, "matched: tests passed") {
			t.Errorf("output %q should report the matched line", got)
		}
	})
}

func TestReportWaitMatchedCov2Status(t *testing.T) {
	withJSONCov2(t, false, func() {
		got := captureStdout(t, func() {
			reportWaitMatched(protocol.WaitMatchedMsg{Status: "stopped"})
		})

		if !strings.Contains(got, "reached status: stopped") {
			t.Errorf("output %q should report the reached status", got)
		}
	})
}

func TestReportWaitMatchedCov2Default(t *testing.T) {
	withJSONCov2(t, false, func() {
		got := captureStdout(t, func() {
			reportWaitMatched(protocol.WaitMatchedMsg{})
		})

		if !strings.Contains(got, "condition met") {
			t.Errorf("output %q should report a generic condition-met message", got)
		}
	})
}

func TestReportWaitMatchedCov2JSON(t *testing.T) {
	withJSONCov2(t, true, func() {
		got := captureStdout(t, func() {
			reportWaitMatched(protocol.WaitMatchedMsg{MatchedLine: "ci green"})
		})

		if !strings.Contains(got, `"matched": true`) {
			t.Errorf("JSON output %q should carry matched=true", got)
		}

		if !strings.Contains(got, "ci green") {
			t.Errorf("JSON output %q should include the matched line", got)
		}
	})
}
