package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func writeScript(t *testing.T, content string) string {
	t.Helper()

	script := filepath.Join(t.TempDir(), "list-models.sh")
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil { //nolint:gosec // G306: script/binary must be executable
		t.Fatal(err)
	}

	return script
}

func TestValidateModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell scripts")
	}

	script := writeScript(t, "#!/bin/sh\necho 'model-a - Model A'\necho 'model-b - Model B'\necho 'model-c - Model C'\n")
	agent := config.Agent{ValidateModel: script}

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
		script := writeScript(t, "#!/bin/sh\necho 'gemini-3.1-pro - Gemini 3.1 Pro (current)'\necho 'gpt-5.5-high - GPT-5.5 1M High'\necho 'grok-4.3 - Grok 4.3 1M'\n")

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

		if !strings.Contains(err.Error(), "cannot resolve") {
			t.Fatalf("expected 'cannot resolve' in error, got: %v", err)
		}
	})

	t.Run("skips header and footer lines", func(t *testing.T) {
		script := writeScript(t, "#!/bin/sh\necho 'Available models'\necho ''\necho 'auto - Auto'\necho 'gpt-5.5-high - GPT-5.5 1M High'\necho ''\necho 'Tip: use --model <id> to switch.'\n")

		a := config.Agent{ValidateModel: script}
		if err := validateModel(a, "auto"); err != nil {
			t.Fatalf("expected no error for valid model, got: %v", err)
		}

		if err := validateModel(a, "gpt-5.5-high"); err != nil {
			t.Fatalf("expected no error for valid model, got: %v", err)
		}

		if err := validateModel(a, "Available models"); err == nil {
			t.Fatal("expected error: header should not be treated as a model")
		}

		if err := validateModel(a, "Tip: use --model <id> to switch."); err == nil {
			t.Fatal("expected error: footer should not be treated as a model")
		}
	})

	t.Run("stderr included in error", func(t *testing.T) {
		script := writeScript(t, "#!/bin/sh\necho 'something went wrong' >&2\nexit 1\n")
		a := config.Agent{ValidateModel: script}

		err := validateModel(a, "any")
		if err == nil {
			t.Fatal("expected error")
		}

		if !strings.Contains(err.Error(), "something went wrong") {
			t.Fatalf("expected stderr in error, got: %v", err)
		}
	})
}
