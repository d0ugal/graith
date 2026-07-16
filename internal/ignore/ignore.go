// Package ignore provides a small, graith-owned interface over a
// Git-compatible gitignore matcher. It wraps
// github.com/go-git/go-git/v5/plumbing/format/gitignore (a maintained
// implementation whose behaviour tracks Git) behind a narrow Matcher interface
// so the rest of graith never depends on a concrete gitignore library.
//
// The interface is deliberately minimal: everything graith needs is "does this
// repo-relative path match the compiled patterns, given whether it is a
// directory". Callers pass slash-separated paths relative to the matcher root;
// the isDir flag lets directory-only patterns (a trailing "/") be honoured
// exactly as Git does, rather than the string-suffix guessing an older matcher
// required.
package ignore

import (
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	gitignore "github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// Matcher reports whether a repo-relative, slash-separated path is matched
// (excluded) by a set of gitignore patterns. A nil-pattern matcher never
// matches.
type Matcher interface {
	// Match reports whether rel matches the patterns. rel is slash-separated
	// and relative to the matcher's root; isDir reports whether it names a
	// directory (which governs directory-only "foo/" patterns).
	Match(rel string, isDir bool) bool
}

// patternMatcher is the go-git-backed implementation of Matcher.
type patternMatcher struct {
	m gitignore.Matcher
}

// Match implements Matcher.
func (p patternMatcher) Match(rel string, isDir bool) bool {
	if p.m == nil {
		return false
	}

	rel = strings.Trim(strings.TrimSpace(rel), "/")
	if rel == "" || rel == "." {
		return false
	}

	return p.m.Match(strings.Split(rel, "/"), isDir)
}

// Lines builds a Matcher from raw .gitignore pattern lines (as they would
// appear inside a .gitignore file). Blank lines and comment lines ("# ...") are
// skipped, matching Git's parser. Patterns are applied in order, so a later
// negation ("!foo") can re-include a path excluded by an earlier line.
func Lines(lines ...string) Matcher {
	return patternMatcher{m: gitignore.NewMatcher(parseLines(nil, lines))}
}

// Dir builds a Matcher from the Git ignore sources rooted at dir, in Git's
// priority order: .git/info/exclude, then the root .gitignore, then any nested
// .gitignore files further down the tree (each scoped to its subdirectory).
// Missing files are not an error — an empty matcher never matches. In a linked
// worktree, where .git is a file rather than a directory, info/exclude is
// simply absent.
func Dir(dir string) Matcher {
	ps, err := gitignore.ReadPatterns(osfs.New(dir), nil)
	if err != nil {
		// ReadPatterns is best-effort: an unreadable subtree yields whatever
		// patterns were gathered so far rather than a hard failure.
		ps = nil
	}

	return patternMatcher{m: gitignore.NewMatcher(ps)}
}

// parseLines turns raw gitignore lines into go-git patterns, skipping blanks
// and comments the way Git's own parser does.
func parseLines(domain []string, lines []string) []gitignore.Pattern {
	var ps []gitignore.Pattern

	for _, l := range lines {
		s := strings.TrimRight(l, "\r")
		if strings.TrimSpace(s) == "" || strings.HasPrefix(s, "#") {
			continue
		}

		ps = append(ps, gitignore.ParsePattern(s, domain))
	}

	return ps
}
