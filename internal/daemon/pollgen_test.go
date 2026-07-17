package daemon

import "github.com/d0ugal/graith/internal/git"

// testPollTools returns a pollTools bundle for tests: a real git Runner (the
// PR-watch tests set up real worktrees for slug/branch resolution) and a stub
// "gh" executable name (the gh calls themselves go through the swapped ghRunner
// seam, which ignores the binary).
func testPollTools() pollTools {
	return pollTools{git: git.NewRunner(), gh: "gh"}
}
