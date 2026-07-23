// Package testprocess identifies Go test binaries at host-mutation boundaries.
// It deliberately links testing.Testing into production code because the Go
// runtime signal is what detects custom-named test executables reliably.
package testprocess

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/agent"
)

// IsGoTestBinary combines the Go runtime's authoritative test-mode signal with
// the conventional executable suffix. The runtime signal covers custom-named
// binaries produced with "go test -o"; the suffix remains a fail-closed backup
// when a test executable is inspected outside the usual test startup path.
func IsGoTestBinary(executable string, reportedByTesting bool) bool {
	return reportedByTesting || strings.HasSuffix(filepath.Base(executable), ".test")
}

// RefuseDaemonLifecycleMutation rejects host daemon/service mutations from a
// Go test process or an agent execution context. Callers use this both before
// orchestration can mutate files or receipts and at the lowest process/service
// primitive available. Tests that exercise the allowed path inject a no-op
// guard only into a private seam; there is deliberately no environment-variable
// or process-global bypass.
func RefuseDaemonLifecycleMutation(operation string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("refusing daemon lifecycle mutation %q: identify current executable: %w", operation, err)
	}

	return refuseDaemonLifecycleMutation(operation, executable, testing.Testing(), os.Environ())
}

func refuseDaemonLifecycleMutation(operation, executable string, reportedByTesting bool, environ []string) error {
	if IsGoTestBinary(executable, reportedByTesting) {
		return fmt.Errorf("refusing daemon lifecycle mutation %q from Go test binary %q", operation, executable)
	}

	if agent.SecurityBoundaryDetectedEnviron(environ) {
		slog.Default().Warn("daemon lifecycle mutation denied", "operation", operation, "reason", "agent execution context")

		return fmt.Errorf("refusing daemon lifecycle mutation %q from an agent execution context", operation)
	}

	return nil
}
