//go:build integration

package integration

import (
	"fmt"
	"os"
	"testing"

	"github.com/d0ugal/graith/internal/cli"
	"github.com/d0ugal/graith/internal/testutil"
)

func TestMain(m *testing.M) {
	// Managed MCP integration tests re-exec this test binary as the real CLI.
	// The production manager resolves its own executable, so handling the child
	// before the test runner starts exercises the normal command registration
	// and `gr mcp-proxy` / `gr mcp` entrypoints without building a second binary.
	if os.Getenv("GRAITH_INTEGRATION_CLI") == "1" {
		if err := cli.Execute(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		os.Exit(0)
	}

	os.Exit(testutil.RunWithIsolatedGit(m))
}
