package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestNewPromptAndPromptFileMutuallyExclusive(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("file prompt"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldPrompt, oldPromptFile := newPrompt, newPromptFile
	oldCfg := cfg
	defer func() {
		newPrompt, newPromptFile = oldPrompt, oldPromptFile
		cfg = oldCfg
	}()

	cfg = config.Default()
	newPrompt = "inline prompt"
	newPromptFile = promptFile

	err := newCmd.RunE(newCmd, []string{"test-session"})
	if err == nil {
		t.Fatal("expected error when both --prompt and --prompt-file are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want it to mention 'mutually exclusive'", err)
	}
}
