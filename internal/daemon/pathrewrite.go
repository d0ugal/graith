package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/d0ugal/graith/internal/atomicfile"
	"github.com/d0ugal/graith/internal/git"
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

// pathSubstitution is a single source→worktree path replacement. needle is one
// spelling of the source path (the resolved absolute path, or its ~/ form);
// worktree is the session worktree path it is replaced with.
type pathSubstitution struct {
	needle   string
	worktree string
}

// buildIncludePathRewrites derives the substitution set for an includes
// session: the main repo plus every included repo, each mapping its source path
// (in both absolute and, when under $HOME, ~/ spellings) to its session
// worktree path. homeDir may be empty (tilde forms are then omitted).
//
// The result is sorted by needle length descending so that when one repo's path
// is an ancestor of another's (a nested included repo), the more specific path
// is matched first and a config reference to the nested repo is not captured by
// its parent. rewritePathsInContent relies on this ordering.
func buildIncludePathRewrites(homeDir, mainRepoPath, mainWorktreePath string, includes []IncludedRepoState) []pathSubstitution {
	subs := make([]pathSubstitution, 0, 2*(len(includes)+1))

	add := func(src, worktree string) {
		if src == "" || worktree == "" {
			return
		}

		src = filepath.Clean(src)
		worktree = filepath.Clean(worktree)

		subs = append(subs, pathSubstitution{needle: src, worktree: worktree})

		if homeDir != "" {
			if rel, err := filepath.Rel(homeDir, src); err == nil &&
				rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
				subs = append(subs, pathSubstitution{needle: "~/" + filepath.ToSlash(rel), worktree: worktree})
			}
		}
	}

	add(mainRepoPath, mainWorktreePath)

	for _, inc := range includes {
		add(inc.RepoPath, inc.WorktreePath)
	}

	// Longest needle first: the most specific path wins at any content position,
	// regardless of the order repos were declared in. Stable so equal-length
	// needles keep declaration order.
	sort.SliceStable(subs, func(i, j int) bool {
		return len(subs[i].needle) > len(subs[j].needle)
	})

	return subs
}

// isPathDelimiter reports whether b is a character that clearly separates a
// filesystem path from surrounding text in the config formats we rewrite
// (dotenv, docker-compose YAML). This is an explicit allowlist of delimiters
// rather than an attempt to enumerate valid filename characters: anything not
// listed is treated as *possibly* part of a path component, so a boundary check
// against it fails and no rewrite happens. That errs toward leaving text alone
// — a missed rewrite falls back to the GRAITH_INCLUDE_*_PATH env var, whereas a
// wrong rewrite would corrupt config — so `~/Code/grafana@next` and
// `~/Code/grafana+ent` are left untouched, not just `-`/`.` siblings.
func isPathDelimiter(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f',
		'=', ':', '"', '\'', '`', ',', ';', '#',
		'(', ')', '[', ']', '{', '}', '<', '>', '|':
		return true
	default:
		return false
	}
}

// isLeadingBoundary reports whether the byte before index idx is a delimiter (or
// idx is the start of the string) — so the match at idx begins a fresh path
// rather than sitting inside a longer one. A path character or '/' before the
// match means it is embedded in a longer path, so it is not a boundary.
func isLeadingBoundary(s string, idx int) bool {
	if idx == 0 {
		return true
	}

	return isPathDelimiter(s[idx-1])
}

// isTrailingBoundary reports whether the match ending at index end is followed
// by a valid path boundary. A trailing '/' is allowed (the path continues into
// the repo — `~/Code/grafana/pkg`), as is a delimiter or end-of-string (the
// path ends there). Any other byte is treated as continuing a longer basename
// (`~/Code/grafana-enterprise`), so the match is rejected.
func isTrailingBoundary(s string, end int) bool {
	if end >= len(s) {
		return true
	}

	return s[end] == '/' || isPathDelimiter(s[end])
}

// rewritePathsInContent replaces every whole-path occurrence of a substitution
// needle with its worktree path, preserving any in-repo suffix (the part after
// the repo root). It scans the original content once left to right, and at each
// position takes the longest needle that matches at a path boundary — so a
// nested repo's path wins over its ancestor's, and a source path that is a
// prefix of a sibling repo's name is left untouched. Scanning the original
// input (never the growing output) means a replacement can never itself be
// re-matched by a later needle.
func rewritePathsInContent(content string, subs []pathSubstitution) (string, bool) {
	if len(subs) == 0 {
		return content, false
	}

	var out strings.Builder

	changed := false
	i := 0

	for i < len(content) {
		matched := false

		for _, sub := range subs {
			if sub.needle == "" {
				continue
			}

			if !strings.HasPrefix(content[i:], sub.needle) {
				continue
			}

			end := i + len(sub.needle)
			if !isLeadingBoundary(content, i) || !isTrailingBoundary(content, end) {
				continue
			}

			out.WriteString(sub.worktree)

			i = end
			changed = true
			matched = true

			break
		}

		if !matched {
			out.WriteByte(content[i])

			i++
		}
	}

	if !changed {
		return content, false
	}

	return out.String(), true
}

// rewriteIncludeConfigPaths scans the known orchestrator config files under each
// worktree root and rewrites absolute source-repo path references to the
// session's worktree paths. It is best-effort: a missing file is normal (the
// override is often gitignored and absent from a fresh checkout) and read or
// write failures are logged and skipped, never fatal to session creation.
//
// Symlinks and other non-regular files are skipped without being read: reading
// through a config symlink could pull the contents of a file outside the
// worktree (a secret) into the agent-readable worktree, and rewriting would
// destroy the link. When a *tracked* file is rewritten it is marked
// skip-worktree so the session-specific path can't be committed accidentally
// and the worktree doesn't start dirty.
func (sm *SessionManager) rewriteIncludeConfigPaths(worktreeRoots []string, subs []pathSubstitution) {
	if len(subs) == 0 {
		return
	}

	for _, root := range worktreeRoots {
		if root == "" {
			continue
		}

		for _, name := range knownIncludeConfigFiles {
			path := filepath.Join(root, name)

			info, err := os.Lstat(path)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					sm.log.Warn("stat include config file", "path", path, "err", err)
				}

				continue
			}

			if !info.Mode().IsRegular() {
				// Symlink, directory, device, etc. — never read or replace it.
				sm.log.Debug("skip non-regular include config file", "path", path, "mode", info.Mode())

				continue
			}

			data, err := os.ReadFile(path)
			if err != nil {
				sm.log.Warn("read include config file", "path", path, "err", err)

				continue
			}

			updated, changed := rewritePathsInContent(string(data), subs)
			if !changed {
				continue
			}

			if err := atomicfile.Write(path, []byte(updated), info.Mode().Perm()); err != nil {
				sm.log.Warn("rewrite include config paths", "path", path, "err", err)

				continue
			}

			sm.markSkipWorktree(root, name)
			sm.log.Info("rewrote include config paths", "path", path)
		}
	}
}

// markSkipWorktree tells git to ignore local modifications to a tracked file, so
// a rewritten session-specific path is neither shown as a dirty change nor
// staged by a blanket `git add`. Best-effort: for an untracked (gitignored)
// file the command fails and is ignored — such a file can't be committed by
// accident anyway.
func (sm *SessionManager) markSkipWorktree(worktreeRoot, relPath string) {
	if !git.RunCheck(worktreeRoot, "update-index", "--skip-worktree", "--", relPath) {
		sm.log.Debug("skip-worktree not applied (file likely untracked)", "root", worktreeRoot, "file", relPath)
	}
}

// applyIncludePathRewrites is the post-worktree hook shared by session creation
// and fork: it derives the source→worktree substitutions for the main repo plus
// every included repo and rewrites the known config files present in any of the
// session's worktrees. A no-op when there are no includes.
func (sm *SessionManager) applyIncludePathRewrites(mainRepoPath, mainWorktreePath string, includes []IncludedRepoState) {
	if len(includes) == 0 {
		return
	}

	home, _ := os.UserHomeDir()
	subs := buildIncludePathRewrites(home, mainRepoPath, mainWorktreePath, includes)

	roots := make([]string, 0, len(includes)+1)
	roots = append(roots, mainWorktreePath)

	for _, inc := range includes {
		roots = append(roots, inc.WorktreePath)
	}

	sm.rewriteIncludeConfigPaths(roots, subs)
}
