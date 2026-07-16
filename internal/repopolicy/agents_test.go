package repopolicy

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	rootAgentsLineLimit = 250
	rootAgentsWordLimit = 2000
)

// TestRootAgentsSizeBudget keeps the always-loaded repository instructions
// concise. Subsystem detail belongs in scoped AGENTS.md files instead.
func TestRootAgentsSizeBudget(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}

	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "AGENTS.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read root AGENTS.md: %v", err)
	}

	lines := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines++
	}

	words := len(strings.Fields(string(data)))
	if lines >= rootAgentsLineLimit || words >= rootAgentsWordLimit {
		t.Fatalf("root AGENTS.md is %d lines / %d words; keep it below %d lines / %d words and move subsystem detail to scoped instructions", lines, words, rootAgentsLineLimit, rootAgentsWordLimit)
	}
}
