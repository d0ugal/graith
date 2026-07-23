package daemon

import (
	"os"
	"testing"

	"github.com/d0ugal/graith/internal/testutil"
)

func TestMain(m *testing.M) {
	configureDaemonTestPTYFactory()

	os.Exit(testutil.RunWithIsolatedGit(m))
}
