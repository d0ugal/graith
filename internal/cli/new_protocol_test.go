package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestCodexOptionsToProtocol(t *testing.T) {
	if got := codexOptionsToProtocol(config.CodexOptions{}); got != nil {
		t.Fatalf("zero options = %+v, want nil", got)
	}

	options := config.CodexOptions{
		Profile: "canny", ReasoningEffort: "high", ServiceTier: "fast", WebSearch: true,
	}
	want := protocol.CodexOptions{
		Profile: "canny", ReasoningEffort: "high", ServiceTier: "fast", WebSearch: true,
	}

	got := codexOptionsToProtocol(options)
	if got == nil || *got != want {
		t.Fatalf("codexOptionsToProtocol(%+v) = %+v, want %+v", options, got, want)
	}
}
