package release_test

import (
	"os"
	"testing"

	"github.com/d0ugal/graith/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithIsolatedGit(m))
}
