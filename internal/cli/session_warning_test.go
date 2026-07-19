package cli

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestUnsandboxedStartWarning(t *testing.T) {
	if got := unsandboxedStartWarning(protocol.SessionInfo{Name: "braw", Sandboxed: true}); got != "" {
		t.Fatalf("sandboxed warning = %q, want empty", got)
	}

	got := unsandboxedStartWarning(protocol.SessionInfo{Name: "canny"})
	for _, want := range []string{"warning:", "canny", "without Graith's sandbox", "external sandbox or VM"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning = %q, want substring %q", got, want)
		}
	}
}
