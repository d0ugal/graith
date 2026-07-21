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

	err := newCmd.RunE(newCmd, []string{"kirk"})
	if err == nil {
		t.Fatal("expected error when both --prompt and --prompt-file are set")
	}

	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want it to mention 'mutually exclusive'", err)
	}
}

func TestNewRejectsInvalidLabelBeforeConnecting(t *testing.T) {
	oldLabels, oldCfg := newLabels, cfg

	t.Cleanup(func() { newLabels, cfg = oldLabels, oldCfg })

	cfg = config.Default()
	newLabels = []string{"   "}

	if err := newCmd.RunE(newCmd, []string{"braw"}); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("new with empty label error = %v", err)
	}
}

func TestNewLabelFlagIsRepeatable(t *testing.T) {
	registerCommands()

	flag := newCmd.Flags().Lookup("label")
	if flag == nil || flag.Value.Type() != "stringArray" {
		t.Fatalf("new --label = %#v, want repeatable stringArray", flag)
	}
}
