// Package covreport turns Go coverage profiles into a per-file Markdown table
// for the PR coverage comment (see .github/workflows/coverage.yml).
//
// Go's `go tool cover -func` reports per-function statement coverage and an
// aggregate total, but not a per-file covered/total breakdown. This package
// parses the raw profile itself — summing statement counts per file — so the
// coverage comment can show which files have the most untested code (sorted by
// uncovered statement count, most first, the most actionable) with a delta
// measured against the PR base.
//
// Coverage in Go is statement-based, not line-based, so the counts here are
// statements. The rendered table labels them accordingly rather than claiming
// "lines".
package covreport

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// FileCov accumulates covered and total statement counts for one file.
type FileCov struct {
	Path    string // repo-relative path, e.g. internal/daemon/handler.go
	Covered int
	Total   int
}

// Pct is the statement-coverage percentage. A file with no statements reports
// 0 rather than dividing by zero.
func (f *FileCov) Pct() float64 {
	if f.Total == 0 {
		return 0
	}

	return 100 * float64(f.Covered) / float64(f.Total)
}

// Parse reads a Go coverage profile and returns per-file statement counts keyed
// by repo-relative path. The module prefix (from go.mod) is stripped from the
// profile's import-path-qualified file names so the table shows repo-relative
// paths. The leading "mode:" line and blank lines are skipped.
//
// Profile block lines have the form:
//
//	<import-path>/file.go:startLine.startCol,endLine.endCol numStatements count
//
// A block counts toward Covered when its execution count is non-zero.
func Parse(r io.Reader, module string) (map[string]*FileCov, error) {
	files := make(map[string]*FileCov)
	sc := bufio.NewScanner(r)
	// Coverage profiles can contain long lines; give the scanner room to grow
	// past the default 64KiB token cap rather than fail on a wide file.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	prefix := strings.TrimSuffix(module, "/") + "/"

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed profile line: %q", line)
		}
		// fields[0] = path:startLine.startCol,endLine.endCol
		// The file path itself never contains a colon, so the first colon
		// separates path from the position spec.
		colon := strings.IndexByte(fields[0], ':')
		if colon < 0 {
			return nil, fmt.Errorf("malformed profile line (no position): %q", line)
		}

		path := fields[0][:colon]

		numStmt, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("bad statement count in %q: %w", line, err)
		}

		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("bad execution count in %q: %w", line, err)
		}
		// Go emits non-negative counts; negatives would produce nonsense
		// covered/total sums, so reject them rather than silently propagate.
		if numStmt < 0 || count < 0 {
			return nil, fmt.Errorf("negative count in %q", line)
		}

		rel := path
		if module != "" && strings.HasPrefix(path, prefix) {
			rel = strings.TrimPrefix(path, prefix)
		}

		fc := files[rel]
		if fc == nil {
			fc = &FileCov{Path: rel}
			files[rel] = fc
		}

		fc.Total += numStmt
		if count > 0 {
			fc.Covered += numStmt
		}
	}

	if err := sc.Err(); err != nil {
		return nil, err
	}

	return files, nil
}

// Row is one file's line in the rendered table, including its delta against the
// base branch.
type Row struct {
	Path    string
	Pct     float64
	Covered int
	Total   int
	Delta   float64 // Pct(head) - Pct(base); meaningful only when InBase
	InBase  bool    // false => new file (no base measurement)
}

// BuildRows joins head and base per-file coverage into table rows, sorted by
// most uncovered statements first (ties broken by path for stable output).
// Ordering by absolute uncovered statement count — not coverage percentage —
// surfaces the files where writing tests buys the most: a 90%-covered
// 500-statement file has more untested code (and more risk) than a 50%-covered
// 4-statement file, yet a percentage sort would bury it. Only files present in
// head are listed — a file that existed at base but was deleted in the PR no
// longer has coverage to report.
func BuildRows(head, base map[string]*FileCov) []Row {
	rows := make([]Row, 0, len(head))
	for path, fc := range head {
		row := Row{
			Path:    path,
			Pct:     fc.Pct(),
			Covered: fc.Covered,
			Total:   fc.Total,
		}
		if b, ok := base[path]; ok {
			row.InBase = true
			row.Delta = fc.Pct() - b.Pct()
		}

		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		ui := rows[i].Total - rows[i].Covered
		uj := rows[j].Total - rows[j].Covered

		if ui != uj {
			return ui > uj
		}

		return rows[i].Path < rows[j].Path
	})

	return rows
}

// RenderTable renders rows as a GitHub-flavoured Markdown table. When
// baseAvailable is false (the base commit failed to build/test) the delta
// column is filled with "—" rather than misleading per-file deltas; when it is
// true, a file absent from the base shows "new". With no rows it returns a
// short italic placeholder so the caller can still embed something meaningful.
func RenderTable(rows []Row, baseAvailable bool) string {
	if len(rows) == 0 {
		return "_No Go coverage data._"
	}

	var b strings.Builder
	b.WriteString("| File | Coverage | Statements | Δ vs base |\n")
	b.WriteString("|------|:--------:|:----------:|:---------:|\n")

	for _, r := range rows {
		var delta string

		switch {
		case !baseAvailable:
			delta = "—"
		case !r.InBase:
			delta = "new"
		default:
			delta = fmt.Sprintf("%+.1f%%", r.Delta)
		}

		fmt.Fprintf(&b, "| %s | %.1f%% | %d/%d | %s |\n",
			r.Path, r.Pct, r.Covered, r.Total, delta)
	}

	return strings.TrimRight(b.String(), "\n")
}

// ModulePath reads the module path from a go.mod file (the token after the
// leading `module` directive). It is best-effort: on any read error or a file
// without a module line it returns an empty string, which makes Parse leave
// import paths unstripped rather than fail the whole report.
func ModulePath(gomod string) string {
	f, err := os.Open(gomod)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Require whitespace after the directive so a hypothetical `moduleXYZ`
		// line can't false-match; mirrors the workflow's `^module[[:space:]]`.
		rest, ok := strings.CutPrefix(line, "module")
		if ok && rest != strings.TrimLeft(rest, " \t") {
			return strings.TrimSpace(rest)
		}
	}

	return ""
}
