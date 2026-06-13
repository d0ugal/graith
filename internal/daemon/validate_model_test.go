package daemon

import (
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
}
