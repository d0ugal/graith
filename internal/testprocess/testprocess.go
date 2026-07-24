// Package testprocess identifies Go test binaries at host-mutation boundaries.
// It deliberately links testing.Testing into production code because the Go
// runtime signal is what detects custom-named test executables reliably.
package testprocess

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// humanLifecycleAuthority is process-local capability state. It is set only
// by code that has successfully read a protected human/service credential;
// it is deliberately not represented in argv, environment, config, or logs.
// A process that did not establish this capability fails closed, including a
// caller that clears all agent markers before invoking a lower-level helper.
var humanLifecycleAuthority atomic.Bool

func markHumanLifecycleAuthority() { humanLifecycleAuthority.Store(true) }

// EstablishHumanLifecycleAuthorityFromFile establishes authority only after a
// protected regular credential file has been opened and validated. Sandboxed
// sessions do not have reachability to this file; unsandboxed same-UID agents
// remain outside this guarantee and lifecycle operations fail closed there.
func EstablishHumanLifecycleAuthorityFromFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()

	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("protected lifecycle credential is invalid")
	}

	data, err := io.ReadAll(f)

	if err != nil || strings.TrimSpace(string(data)) == "" {
		return errors.New("protected lifecycle credential is invalid")
	}

	humanLifecycleAuthority.Store(true)

	return nil
}

func resetHumanLifecycleAuthority() { humanLifecycleAuthority.Store(false) }

// IsGoTestBinary combines the Go runtime's authoritative test-mode signal with
// the conventional executable suffix. The runtime signal covers custom-named
// binaries produced with "go test -o"; the suffix remains a fail-closed backup
// when a test executable is inspected outside the usual test startup path.
func IsGoTestBinary(executable string, reportedByTesting bool) bool {
	return reportedByTesting || strings.HasSuffix(filepath.Base(executable), ".test")
}

// RefuseDaemonLifecycleMutation rejects host daemon/service mutations unless
// this process established positive human lifecycle authority. Environment
// markers are intentionally irrelevant: they are mutable by a session.
func RefuseDaemonLifecycleMutation(operation string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("refusing daemon lifecycle mutation %q: identify current executable: %w", operation, err)
	}

	return refuseDaemonLifecycleMutation(operation, executable, testing.Testing())
}

func refuseDaemonLifecycleMutation(operation, executable string, reportedByTesting bool) error {
	if IsGoTestBinary(executable, reportedByTesting) {
		return fmt.Errorf("refusing daemon lifecycle mutation %q from Go test binary %q", operation, executable)
	}

	if !humanLifecycleAuthority.Load() {
		slog.Default().Warn("daemon lifecycle mutation denied", "operation", operation, "reason", "missing positive human authority")

		return fmt.Errorf("refusing daemon lifecycle mutation %q: positive human lifecycle authority is required", operation)
	}

	return nil
}
