package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestValidateModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses printf")
	}

	agent := config.Agent{
		ValidateModel: "printf model-a\\nmodel-b\\nmodel-c\\n",
	}

	t.Run("valid model", func(t *testing.T) {
		if err := validateModel(agent, "model-b"); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	t.Run("invalid model", func(t *testing.T) {
		err := validateModel(agent, "model-z")
		if err == nil {
			t.Fatal("expected error for invalid model")
		}
		if !strings.Contains(err.Error(), "invalid model") {
			t.Fatalf("expected 'invalid model' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "model-a") {
			t.Fatalf("expected valid models listed in error, got: %v", err)
		}
	})

	t.Run("model with description suffix", func(t *testing.T) {
		script := filepath.Join(t.TempDir(), "list-models.sh")
		os.WriteFile(script, []byte("#!/bin/sh\necho 'gemini-3.1-pro - Gemini 3.1 Pro (current)'\necho 'gpt-5.5-high - GPT-5.5 1M High'\necho 'grok-4.3 - Grok 4.3 1M'\n"), 0o755)
		described := config.Agent{ValidateModel: script}
		if err := validateModel(described, "gemini-3.1-pro"); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if err := validateModel(described, "grok-4.3"); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		err := validateModel(described, "nonexistent")
		if err == nil {
			t.Fatal("expected error for invalid model")
		}
	})

	t.Run("empty model skips validation", func(t *testing.T) {
		if err := validateModel(agent, ""); err != nil {
			t.Fatalf("expected no error for empty model, got: %v", err)
		}
	})

	t.Run("no validate_model skips validation", func(t *testing.T) {
		noValidate := config.Agent{}
		if err := validateModel(noValidate, "anything"); err != nil {
			t.Fatalf("expected no error without validate_model, got: %v", err)
		}
	})

	t.Run("command not found", func(t *testing.T) {
		bad := config.Agent{ValidateModel: "nonexistent-binary-xyz --list"}
		err := validateModel(bad, "some-model")
		if err == nil {
			t.Fatal("expected error for missing binary")
		}
		if !strings.Contains(err.Error(), "validate model") {
			t.Fatalf("expected 'validate model' in error, got: %v", err)
		}
	})
}
