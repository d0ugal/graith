package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// restoreCLIGlobals snapshots the mutable package-level CLI state that
// executeWithArgs writes to, so man tests don't leak JSON/agent-mode state into
// sibling tests that assert on stderr formatting.
func restoreCLIGlobals(t *testing.T) {
	t.Helper()

	origOut := out
	origJSON := jsonOutput
	origAgentMode := agentMode

	t.Cleanup(func() {
		out = origOut
		jsonOutput = origJSON
		agentMode = origAgentMode
	})
}

func TestManGeneratesRootPage(t *testing.T) {
	restoreCLIGlobals(t)
	registerCommands()

	outdir := t.TempDir()

	if err := executeWithArgs([]string{"man", outdir}); err != nil {
		t.Fatalf("man command failed: %v", err)
	}

	manPath := filepath.Join(outdir, "gr.1")

	data, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatalf("expected man page at %s: %v", manPath, err)
	}

	content := string(data)

	// The man page must carry the roff title header for section 1 and name gr.
	if !strings.Contains(content, `.TH "GR" "1"`) {
		t.Errorf("man page missing expected roff title header")
	}

	if !strings.Contains(content, "graith") {
		t.Errorf("man page missing source/manual name %q", "graith")
	}
}

// TestManGeneratesSubcommandPagesReferencedBySeeAlso is the CLI-side half of
// the #779 fix: the root gr.1 carries a SEE ALSO block cross-referencing
// per-subcommand pages, and those pages must actually be generated alongside it.
// Packaging (see internal/release) then ships the whole tree so the references
// resolve on the target system.
func TestManGeneratesSubcommandPagesReferencedBySeeAlso(t *testing.T) {
	restoreCLIGlobals(t)
	registerCommands()

	outdir := t.TempDir()

	if err := executeWithArgs([]string{"man", outdir}); err != nil {
		t.Fatalf("man command failed: %v", err)
	}

	root, err := os.ReadFile(filepath.Join(outdir, "gr.1"))
	if err != nil {
		t.Fatalf("expected root man page: %v", err)
	}

	if !strings.Contains(string(root), "SEE ALSO") {
		t.Fatal("root gr.1 has no SEE ALSO section; packaging assumption in #779 no longer holds")
	}

	// Every gr-*(N) reference the root page makes must resolve to a generated
	// gr-*.N page — that is exactly the invariant #779 broke when packaging
	// shipped only gr.1. Parsing all references (not sampling one) means the
	// test fails if cobra ever emits a dangling cross-reference the packaging
	// glob wouldn't cover.
	refs := manPageRefs(string(root))
	if len(refs) == 0 {
		t.Fatal("root gr.1 references no subcommand pages; packaging assumption in #779 no longer holds")
	}

	for _, ref := range refs {
		if _, err := os.Stat(filepath.Join(outdir, ref)); err != nil {
			t.Errorf("root gr.1 SEE ALSO references %q but that page was not generated: %v", ref, err)
		}
	}
}

// manPageRefsPattern matches roff man-page cross-references like
// \fBgr-msg(1)\fP, capturing the page name and section so we can map each to its
// generated file (gr-msg.1). Only gr-* references are of interest — the root
// page's SEE ALSO is what #779 is about.
var manPageRefsPattern = regexp.MustCompile(`(gr-[a-z0-9-]+)\((\d)\)`)

// manPageRefs returns the deduplicated set of generated page filenames (e.g.
// "gr-msg.1") referenced by roff cross-references in the given man page content.
func manPageRefs(content string) []string {
	seen := make(map[string]struct{})

	var refs []string

	for _, m := range manPageRefsPattern.FindAllStringSubmatch(content, -1) {
		file := m[1] + "." + m[2]
		if _, ok := seen[file]; ok {
			continue
		}

		seen[file] = struct{}{}
		refs = append(refs, file)
	}

	return refs
}

func TestManRequiresOutdirArg(t *testing.T) {
	restoreCLIGlobals(t)
	registerCommands()

	if err := executeWithArgs([]string{"man"}); err == nil {
		t.Fatal("expected error when outdir arg is missing")
	}
}
