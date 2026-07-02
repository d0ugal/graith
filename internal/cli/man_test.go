package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// restoreCLIGlobals snapshots the mutable package-level CLI state that
// executeWithArgs writes to, so man tests don't leak JSON/agent-mode state into
// sibling tests that assert on stderr formatting.
func restoreCLIGlobals(t *testing.T) {
	t.Helper()

	origOut := out
	origJSON := jsonOutput
	origAgentMode := agentMode

	t.Cleanup(func() {
		out = origOut
		jsonOutput = origJSON
		agentMode = origAgentMode
	})
}

func TestManGeneratesRootPage(t *testing.T) {
	restoreCLIGlobals(t)
	registerCommands()

	outdir := t.TempDir()

	if err := executeWithArgs([]string{"man", outdir}); err != nil {
		t.Fatalf("man command failed: %v", err)
	}

	manPath := filepath.Join(outdir, "gr.1")

	data, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatalf("expected man page at %s: %v", manPath, err)
	}

	content := string(data)

	// The man page must carry the roff title header for section 1 and name gr.
	if !strings.Contains(content, `.TH "GR" "1"`) {
		t.Errorf("man page missing expected roff title header")
	}

	if !strings.Contains(content, "graith") {
		t.Errorf("man page missing source/manual name %q", "graith")
	}
}

func TestManRequiresOutdirArg(t *testing.T) {
	restoreCLIGlobals(t)
	registerCommands()

	if err := executeWithArgs([]string{"man"}); err == nil {
		t.Fatal("expected error when outdir arg is missing")
	}
}
