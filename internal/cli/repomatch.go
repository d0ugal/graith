package cli

import (
	"strings"

	"github.com/d0ugal/graith/internal/protocol"
)

// matchesRepo reports whether a session belongs to the repo identified by the
// --repo filter value. The match is flexible so that `gr list --repo X` and the
// batch commands (`gr stop`/`gr delete --repo X`) select the same sessions
// (issue #202). A session matches when the value equals the full repo path, is a
// trailing path segment of the repo path (e.g. "graith" matches
// "/home/me/Code/graith"), or equals the repo's short name.
func matchesRepo(s protocol.SessionInfo, value string) bool {
	return s.RepoPath == value ||
		strings.HasSuffix(s.RepoPath, "/"+value) ||
		s.RepoName == value
}
