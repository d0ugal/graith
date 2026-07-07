package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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

func TestSourceDate(t *testing.T) {
	t.Run("unset returns nil", func(t *testing.T) {
		t.Setenv("SOURCE_DATE_EPOCH", "")
		os.Unsetenv("SOURCE_DATE_EPOCH")

		if got := sourceDate(); got != nil {
			t.Fatalf("sourceDate() = %v, want nil when unset", got)
		}
	})

	t.Run("invalid returns nil", func(t *testing.T) {
		t.Setenv("SOURCE_DATE_EPOCH", "haar")

		if got := sourceDate(); got != nil {
			t.Fatalf("sourceDate() = %v, want nil for non-numeric value", got)
		}
	})

	t.Run("valid epoch parses to fixed UTC time", func(t *testing.T) {
		// 1000000000 = 2001-09-09T01:46:40 in UTC.
		t.Setenv("SOURCE_DATE_EPOCH", "1000000000")

		got := sourceDate()
		if got == nil {
			t.Fatal("sourceDate() = nil, want a time")
		}

		// Must be normalized to UTC, not the build machine's local zone —
		// the .TH date renders as month+year, so a local zone could shift a
		// commit near a month boundary into a different month.
		if got.Location() != time.UTC {
			t.Errorf("sourceDate() location = %v, want UTC", got.Location())
		}

		if want := time.Unix(1000000000, 0).UTC(); !got.Equal(want) {
			t.Errorf("sourceDate() = %v, want %v", got, want)
		}
	})
}

// TestManDeterministicDate verifies the generated man page embeds the pinned
// SOURCE_DATE_EPOCH date (not wall-clock time) and omits cobra's auto-generated
// HISTORY footer, so rebuilds of the same commit are byte-for-byte identical.
func TestManDeterministicDate(t *testing.T) {
	restoreCLIGlobals(t)
	registerCommands()

	t.Setenv("SOURCE_DATE_EPOCH", "1000000000") // Sep 2001

	outdir := t.TempDir()

	if err := executeWithArgs([]string{"man", outdir}); err != nil {
		t.Fatalf("man command failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outdir, "gr.1"))
	if err != nil {
		t.Fatalf("read gr.1: %v", err)
	}

	content := string(data)

	if !strings.Contains(content, `"Sep 2001"`) {
		t.Errorf("man page .TH line does not embed pinned SOURCE_DATE_EPOCH date; got:\n%s", firstLineWithPrefix(content, ".TH"))
	}

	if strings.Contains(content, "Auto generated by spf13/cobra") {
		t.Error("man page still contains cobra HISTORY footer; DisableAutoGenTag not applied")
	}
}

// TestManReproducible directly encodes the issue #778 contract: two runs at the
// same SOURCE_DATE_EPOCH must produce byte-for-byte identical man pages, so the
// packaged gr.1.gz hash is stable across rebuilds of the same commit.
func TestManReproducible(t *testing.T) {
	restoreCLIGlobals(t)
	registerCommands()

	t.Setenv("SOURCE_DATE_EPOCH", "1000000000")

	dirA := t.TempDir()
	dirB := t.TempDir()

	if err := executeWithArgs([]string{"man", dirA}); err != nil {
		t.Fatalf("man command (A) failed: %v", err)
	}

	if err := executeWithArgs([]string{"man", dirB}); err != nil {
		t.Fatalf("man command (B) failed: %v", err)
	}

	pageA, err := os.ReadFile(filepath.Join(dirA, "gr.1"))
	if err != nil {
		t.Fatalf("read gr.1 (A): %v", err)
	}

	pageB, err := os.ReadFile(filepath.Join(dirB, "gr.1"))
	if err != nil {
		t.Fatalf("read gr.1 (B): %v", err)
	}

	if !bytes.Equal(pageA, pageB) {
		t.Error("gr.1 differs between two runs at the same SOURCE_DATE_EPOCH; man generation is not reproducible")
	}
}

// TestManInvalidSourceDateEpochFailsClosed documents that a garbage
// SOURCE_DATE_EPOCH is rejected (cobra re-reads and validates it) rather than
// silently falling back to wall-clock time.
func TestManInvalidSourceDateEpochFailsClosed(t *testing.T) {
	restoreCLIGlobals(t)
	registerCommands()

	t.Setenv("SOURCE_DATE_EPOCH", "haar")

	if err := executeWithArgs([]string{"man", t.TempDir()}); err == nil {
		t.Fatal("expected error for invalid SOURCE_DATE_EPOCH, got nil")
	}
}

func firstLineWithPrefix(s, prefix string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}

	return "(no line with prefix " + prefix + ")"
}
