// Package testprocess identifies Go test binaries at host-mutation boundaries.
package testprocess

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// IsGoTestBinary combines the Go runtime's authoritative test-mode signal with
// the conventional executable suffix. The runtime signal covers custom-named
// binaries produced with "go test -o"; the suffix remains a fail-closed backup
// when a test executable is inspected outside the usual test startup path.
func IsGoTestBinary(executable string, reportedByTesting bool) bool {
	return reportedByTesting || strings.HasSuffix(filepath.Base(executable), ".test")
}

// RefuseDaemonLifecycleMutation rejects host daemon/service mutations from a
// Go test process. Callers use this both before orchestration can mutate files
// or receipts and at the lowest process/service primitive available. Tests that
// exercise the allowed path inject a no-op guard only into a private seam; there
// is deliberately no environment-variable or process-global bypass.
func RefuseDaemonLifecycleMutation(operation string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("refusing daemon lifecycle mutation %q: identify current executable: %w", operation, err)
	}

	if IsGoTestBinary(executable, testing.Testing()) {
		return fmt.Errorf("refusing daemon lifecycle mutation %q from Go test binary %q", operation, executable)
	}

	return nil
}
