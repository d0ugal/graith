package cli

import (
	"os"
	"testing"

	"github.com/d0ugal/graith/internal/testutil"
)

func TestMain(m *testing.M) {
	code := testutil.RunWithIsolatedGraith(func() int {
		return testutil.RunWithIsolatedGit(m)
	})

	os.Exit(code)
}
