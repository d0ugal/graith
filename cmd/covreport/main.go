// Command covreport renders a per-file Go coverage table (Markdown) for the PR
// coverage comment. It reads a head coverage profile and, optionally, a base
// profile to compute per-file deltas, then writes the table to stdout.
//
// Usage:
//
//	covreport -head cover.head.out [-base cover.base.out] [-module github.com/d0ugal/graith]
//
// If -module is empty it is read from ./go.mod. A missing or empty -base is
// treated as "base unavailable" — the table still renders, with "—" deltas.
// The tool never exits non-zero for a missing base or an empty profile so a
// coverage-comment step can rely on it; it exits non-zero only when a supplied
// profile is genuinely malformed.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/d0ugal/graith/internal/covreport"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run holds the CLI logic behind main so it can be unit-tested: args, streams,
// and exit code are all injectable. Exit codes: 0 success, 1 a malformed
// profile, 2 a usage error.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("covreport", flag.ContinueOnError)
	fs.SetOutput(stderr)
	head := fs.String("head", "", "path to the head coverage profile (required)")
	base := fs.String("base", "", "path to the base coverage profile (optional)")

	module := fs.String("module", "", "module path to strip (default: read from ./go.mod)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *head == "" {
		_, _ = fmt.Fprintln(stderr, "covreport: -head is required")
		return 2
	}

	mod := *module
	if mod == "" {
		mod = covreport.ModulePath("go.mod")
	}

	headCov, err := parseFile(*head, mod)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "covreport: %v\n", err)
		return 1
	}

	// A missing base profile is not an error: the base commit may have failed
	// to build/test. Only a base that exists and is malformed is fatal. Base
	// availability is decided by *parsed content*, not file size — a failed
	// `go test -coverprofile` leaves a header-only ("mode: set\n") profile that
	// is non-empty on disk but yields zero files; treating that as "available"
	// would mislabel every head file "new" instead of "—".
	var baseCov map[string]*covreport.FileCov

	if *base != "" {
		if _, statErr := os.Stat(*base); statErr == nil {
			baseCov, err = parseFile(*base, mod)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "covreport: %v\n", err)
				return 1
			}
		}
	}

	baseAvailable := len(baseCov) > 0

	rows := covreport.BuildRows(headCov, baseCov)
	_, _ = fmt.Fprintln(stdout, covreport.RenderTable(rows, baseAvailable))

	return 0
}

func parseFile(path, module string) (map[string]*covreport.FileCov, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	return covreport.Parse(f, module)
}
