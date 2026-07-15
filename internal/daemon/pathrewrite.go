package daemon

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/d0ugal/graith/internal/atomicfile"
)

// knownIncludeConfigFiles are the orchestrator config files scanned for
// absolute source-repo path references after an includes session's worktrees
// are set up. Each name is resolved relative to a worktree root.
//
// These files are the standard places a dev-env orchestrator hard-codes an
// absolute sibling-repo path (a docker-compose bind mount, an env var pointing
// at a repo). Rewriting them to the session's worktree paths lets an
// orchestrator that can't use the GRAITH_INCLUDE_*_PATH env vars still see the
// session's in-progress code instead of the main checkout. Files that are
// gitignored (so absent from a fresh worktree checkout) are simply skipped —
// there is nothing to rewrite — and the env-var mitigation still applies.
var knownIncludeConfigFiles = []string{
	".env.local",
	"docker-compose.override.yml",
}

// pathRewrite is a single source→worktree substitution. sourceForms holds every
// spelling of the source path a config file might use (the resolved absolute
// path and, when under $HOME, the tilde form), longest first so a more specific
// form wins before a prefix of it.
type pathRewrite struct {
	worktree    string
	sourceForms []string
}

// buildIncludePathRewrites derives the substitution set for an includes
// session: the main repo plus every included repo, each mapping its source path
// to its session worktree path. homeDir may be empty (tilde forms are then
// omitted).
func buildIncludePathRewrites(homeDir, mainRepoPath, mainWorktreePath string, includes []IncludedRepoState) []pathRewrite {
	rewrites := make([]pathRewrite, 0, len(includes)+1)

	add := func(src, worktree string) {
		if src == "" || worktree == "" {
			return
		}

		src = filepath.Clean(src)
		forms := []string{src}

		if homeDir != "" {
			if rel, err := filepath.Rel(homeDir, src); err == nil &&
				rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
				forms = append(forms, "~/"+filepath.ToSlash(rel))
			}
		}

		rewrites = append(rewrites, pathRewrite{
			worktree:    filepath.Clean(worktree),
			sourceForms: forms,
		})
	}

	add(mainRepoPath, mainWorktreePath)

	for _, inc := range includes {
		add(inc.RepoPath, inc.WorktreePath)
	}

	return rewrites
}

// isPathComponentChar reports whether b can be part of a path basename — a
// letter, digit, or one of the common in-name punctuation characters. A source
// path immediately followed by one of these is a longer sibling name
// (`~/Code/grafana` vs `~/Code/grafana-enterprise`) and must not be rewritten.
func isPathComponentChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-' || b == '.':
		return true
	default:
		return false
	}
}

// isLeadingBoundary reports whether the byte before index idx is a boundary — a
// character that can't be part of a path — so the match at idx starts a fresh
// path rather than sitting inside a longer one. Start-of-string is a boundary.
func isLeadingBoundary(s string, idx int) bool {
	if idx == 0 {
		return true
	}

	b := s[idx-1]
	// Anything that could extend the path leftward is not a boundary: path
	// component chars plus the separators '/' and '~'.
	return !isPathComponentChar(b) && b != '/' && b != '~'
}

// isTrailingBoundary reports whether the match ending at index end is followed
// by a valid path boundary. A trailing '/' is allowed (the path continues into
// the repo — `~/Code/grafana/pkg`), as is any non-component byte (end of the
// path). A trailing component char means a longer basename, so it is rejected.
func isTrailingBoundary(s string, end int) bool {
	if end >= len(s) {
		return true
	}

	b := s[end]
	if b == '/' {
		return true
	}

	return !isPathComponentChar(b)
}

// rewritePathsInContent replaces every whole-path occurrence of each rewrite's
// source forms with its worktree path, preserving any in-repo suffix (the part
// after the repo root). It returns the rewritten content and whether anything
// changed. Matches are only made at path boundaries, so a source path that is a
// prefix of a sibling repo's name is left untouched.
func rewritePathsInContent(content string, rewrites []pathRewrite) (string, bool) {
	changed := false
	result := content

	for _, rw := range rewrites {
		for _, needle := range rw.sourceForms {
			if needle == "" {
				continue
			}

			var out strings.Builder

			i := 0

			for {
				rel := strings.Index(result[i:], needle)
				if rel < 0 {
					out.WriteString(result[i:])
					break
				}

				idx := i + rel
				end := idx + len(needle)

				if isLeadingBoundary(result, idx) && isTrailingBoundary(result, end) {
					out.WriteString(result[i:idx])
					out.WriteString(rw.worktree)

					changed = true
				} else {
					out.WriteString(result[i:end])
				}

				i = end
			}

			result = out.String()
		}
	}

	return result, changed
}

// rewriteIncludeConfigPaths scans the known orchestrator config files under each
// worktree root and rewrites absolute source-repo path references to the
// session's worktree paths. It is best-effort: a missing file is normal (the
// override is often gitignored and absent from a fresh checkout) and a read or
// write failure is logged and skipped, never fatal to session creation.
func (sm *SessionManager) rewriteIncludeConfigPaths(worktreeRoots []string, rewrites []pathRewrite) {
	if len(rewrites) == 0 {
		return
	}

	for _, root := range worktreeRoots {
		if root == "" {
			continue
		}

		for _, name := range knownIncludeConfigFiles {
			path := filepath.Join(root, name)

			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			updated, changed := rewritePathsInContent(string(data), rewrites)
			if !changed {
				continue
			}

			mode := os.FileMode(0o644)
			if info, statErr := os.Stat(path); statErr == nil {
				mode = info.Mode().Perm()
			}

			if err := atomicfile.Write(path, []byte(updated), mode); err != nil {
				sm.log.Warn("rewrite include config paths", "path", path, "err", err)

				continue
			}

			sm.log.Info("rewrote include config paths", "path", path)
		}
	}
}
