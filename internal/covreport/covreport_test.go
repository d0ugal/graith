package covreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/d0ugal/graith"

// A small two-file profile in `set` mode. The braw file is fully covered; the
// dreich file is half covered (one block hit, one missed).
const brawProfile = `mode: set
github.com/d0ugal/graith/internal/braw/braw.go:10.2,12.3 2 1
github.com/d0ugal/graith/internal/braw/braw.go:15.2,16.3 1 1
github.com/d0ugal/graith/internal/dreich/dreich.go:20.2,22.3 3 1
github.com/d0ugal/graith/internal/dreich/dreich.go:25.2,27.3 3 0
`

func TestParseSumsPerFile(t *testing.T) {
	files, err := Parse(strings.NewReader(brawProfile), module)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}

	braw := files["internal/braw/braw.go"]
	if braw == nil {
		t.Fatal("braw.go missing (module prefix not stripped?)")
	}

	if braw.Covered != 3 || braw.Total != 3 {
		t.Errorf("braw: covered/total = %d/%d, want 3/3", braw.Covered, braw.Total)
	}

	if braw.Pct() != 100 {
		t.Errorf("braw pct = %v, want 100", braw.Pct())
	}

	dreich := files["internal/dreich/dreich.go"]
	if dreich == nil {
		t.Fatal("dreich.go missing")
	}

	if dreich.Covered != 3 || dreich.Total != 6 {
		t.Errorf("dreich: covered/total = %d/%d, want 3/6", dreich.Covered, dreich.Total)
	}

	if dreich.Pct() != 50 {
		t.Errorf("dreich pct = %v, want 50", dreich.Pct())
	}
}

func TestPctZeroWhenNoStatements(t *testing.T) {
	fc := &FileCov{Path: "internal/neep/neep.go"}
	if got := fc.Pct(); got != 0 {
		t.Errorf("Pct with no statements = %v, want 0", got)
	}
}

func TestParseSkipsModeAndBlankLines(t *testing.T) {
	in := "mode: atomic\n\ngithub.com/d0ugal/graith/a.go:1.1,2.2 1 1\n\n"

	files, err := Parse(strings.NewReader(in), module)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}

	if files["a.go"] == nil {
		t.Errorf("a.go missing: %v", files)
	}
}

func TestParseKeepsPathWhenModuleMismatch(t *testing.T) {
	// A path outside the module (or an empty module) is left unstripped rather
	// than mangled.
	in := "mode: set\nexample.com/other/x.go:1.1,2.2 1 1\n"

	files, err := Parse(strings.NewReader(in), module)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if files["example.com/other/x.go"] == nil {
		t.Errorf("unstripped path missing: %v", files)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"too few fields": "mode: set\ngithub.com/d0ugal/graith/a.go:1.1,2.2 1\n",
		"no colon":       "mode: set\nnoposition 1 1\n",
		"bad statements": "mode: set\ngithub.com/d0ugal/graith/a.go:1.1,2.2 x 1\n",
		"bad exec count": "mode: set\ngithub.com/d0ugal/graith/a.go:1.1,2.2 1 y\n",
		"negative stmts": "mode: set\ngithub.com/d0ugal/graith/a.go:1.1,2.2 -1 1\n",
		"negative count": "mode: set\ngithub.com/d0ugal/graith/a.go:1.1,2.2 1 -1\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(in), module); err == nil {
				t.Errorf("want error for %q, got nil", name)
			}
		})
	}
}

func TestBuildRowsSortsMostUncoveredFirst(t *testing.T) {
	// Coverage percentage and uncovered-statement count disagree here, so the
	// full ordering is only correct under the uncovered-count key: braw has the
	// highest percentage yet the most untested code, and fash the lowest
	// percentage yet the least. A percentage sort would produce fash, dreich,
	// braw — the exact reverse of the head/tail asserted below.
	head := map[string]*FileCov{
		"internal/braw/braw.go":     {Path: "internal/braw/braw.go", Covered: 90, Total: 100},   // 90%, 10 uncovered
		"internal/dreich/dreich.go": {Path: "internal/dreich/dreich.go", Covered: 1, Total: 10}, // 10%, 9 uncovered
		"internal/fash/fash.go":     {Path: "internal/fash/fash.go", Covered: 3, Total: 6},      // 50%, 3 uncovered
	}

	rows := BuildRows(head, nil)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}

	if rows[0].Path != "internal/braw/braw.go" {
		t.Errorf("first row = %s, want most-uncovered braw.go (10 uncovered)", rows[0].Path)
	}

	if rows[2].Path != "internal/fash/fash.go" {
		t.Errorf("last row = %s, want least-uncovered fash.go (3 uncovered)", rows[2].Path)
	}
}

// Regression: the sort key is uncovered statement count, not coverage
// percentage. A big, mostly-covered file has more untested code than a tiny,
// poorly-covered one and must sort first — a percentage sort would invert this.
func TestBuildRowsRanksByUncoveredNotPercentage(t *testing.T) {
	head := map[string]*FileCov{
		// 50% coverage but only 2 uncovered statements — low percentage.
		"internal/skelf/skelf.go": {Path: "internal/skelf/skelf.go", Covered: 2, Total: 4},
		// 90% coverage but 10 uncovered statements — high percentage, more debt.
		"internal/strath/strath.go": {Path: "internal/strath/strath.go", Covered: 90, Total: 100},
	}

	rows := BuildRows(head, nil)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}

	// A percentage sort would put skelf (50%) first; the uncovered-count sort
	// puts strath (10 uncovered) first.
	if rows[0].Path != "internal/strath/strath.go" {
		t.Errorf("first row = %s, want most-uncovered strath.go (10 uncovered) ahead of skelf.go (2 uncovered)", rows[0].Path)
	}
}

func TestBuildRowsTieBreaksByPath(t *testing.T) {
	// Both files have 2 uncovered statements but different percentages, and the
	// lexically-first path is the higher-percentage one — so a percentage sort
	// would order z.go (50%) before a.go (80%). Equal uncovered counts must fall
	// through to path order (a.go then z.go), proving the tie-break is on path,
	// not percentage.
	head := map[string]*FileCov{
		"internal/wynd/a.go": {Path: "internal/wynd/a.go", Covered: 8, Total: 10}, // 80%, 2 uncovered
		"internal/wynd/z.go": {Path: "internal/wynd/z.go", Covered: 2, Total: 4},  // 50%, 2 uncovered
	}

	rows := BuildRows(head, nil)
	if rows[0].Path != "internal/wynd/a.go" || rows[1].Path != "internal/wynd/z.go" {
		t.Errorf("equal uncovered count not tie-broken by path: %s then %s", rows[0].Path, rows[1].Path)
	}
}

// A file with no statements has no untested code, so it sorts to the bottom
// (0 uncovered) rather than the top. Under the old percentage key its Pct() of
// 0 sorted it up with the worst-covered files — the opposite, and unhelpful.
func TestBuildRowsZeroStatementFileSortsLast(t *testing.T) {
	head := map[string]*FileCov{
		"internal/neep/neep.go": {Path: "internal/neep/neep.go", Covered: 0, Total: 0}, // no statements
		"internal/fash/fash.go": {Path: "internal/fash/fash.go", Covered: 1, Total: 5}, // 4 uncovered
	}

	rows := BuildRows(head, nil)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}

	if rows[len(rows)-1].Path != "internal/neep/neep.go" {
		t.Errorf("zero-statement file = %s at bottom, want internal/neep/neep.go", rows[len(rows)-1].Path)
	}
}

func TestBuildRowsDelta(t *testing.T) {
	head := map[string]*FileCov{
		"internal/bide/bide.go": {Path: "internal/bide/bide.go", Covered: 3, Total: 4}, // 75%
		"internal/hame/hame.go": {Path: "internal/hame/hame.go", Covered: 1, Total: 2}, // 50%, new
	}
	base := map[string]*FileCov{
		"internal/bide/bide.go": {Path: "internal/bide/bide.go", Covered: 2, Total: 4}, // 50%
	}
	rows := BuildRows(head, base)

	byPath := map[string]Row{}
	for _, r := range rows {
		byPath[r.Path] = r
	}

	bide := byPath["internal/bide/bide.go"]
	if !bide.InBase {
		t.Error("bide should be marked InBase")
	}

	if bide.Delta != 25 {
		t.Errorf("bide delta = %v, want 25", bide.Delta)
	}

	hame := byPath["internal/hame/hame.go"]
	if hame.InBase {
		t.Error("hame is new; should not be InBase")
	}
}

func TestBuildRowsIgnoresDeletedBaseFile(t *testing.T) {
	// A file present at base but gone from head must not appear.
	head := map[string]*FileCov{
		"internal/braw/braw.go": {Path: "internal/braw/braw.go", Covered: 1, Total: 1},
	}
	base := map[string]*FileCov{
		"internal/braw/braw.go": {Path: "internal/braw/braw.go", Covered: 1, Total: 1},
		"internal/auld/gone.go": {Path: "internal/auld/gone.go", Covered: 0, Total: 5},
	}

	rows := BuildRows(head, base)
	if len(rows) != 1 {
		t.Fatalf("want 1 row (deleted base file excluded), got %d", len(rows))
	}
}

func TestRenderTableFull(t *testing.T) {
	rows := []Row{
		{Path: "internal/fash/fash.go", Pct: 10, Covered: 1, Total: 10, InBase: true, Delta: -1.5},
		{Path: "internal/hame/hame.go", Pct: 50, Covered: 1, Total: 2, InBase: false},
	}

	out := RenderTable(rows, true)
	if !strings.Contains(out, "| internal/fash/fash.go | 10.0% | 1/10 | -1.5% |") {
		t.Errorf("missing formatted delta row:\n%s", out)
	}

	if !strings.Contains(out, "| internal/hame/hame.go | 50.0% | 1/2 | new |") {
		t.Errorf("new file should show 'new':\n%s", out)
	}

	if !strings.HasPrefix(out, "| File | Coverage | Statements | Δ vs base |") {
		t.Errorf("missing header:\n%s", out)
	}
}

func TestRenderTableBaseUnavailable(t *testing.T) {
	rows := []Row{{Path: "internal/fash/fash.go", Pct: 10, Covered: 1, Total: 10, InBase: false}}

	out := RenderTable(rows, false)
	if !strings.Contains(out, "| internal/fash/fash.go | 10.0% | 1/10 | — |") {
		t.Errorf("base-unavailable delta should be em dash:\n%s", out)
	}
}

func TestRenderTableEmpty(t *testing.T) {
	if got := RenderTable(nil, true); got != "_No Go coverage data._" {
		t.Errorf("empty render = %q", got)
	}
}

func TestModulePath(t *testing.T) {
	dir := t.TempDir()

	gomod := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(gomod, []byte("module github.com/d0ugal/croft\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := ModulePath(gomod); got != "github.com/d0ugal/croft" {
		t.Errorf("ModulePath = %q, want github.com/d0ugal/croft", got)
	}
}

func TestModulePathRejectsPrefixOnlyMatch(t *testing.T) {
	// A line like `moduleXYZ ...` must not false-match the `module` directive.
	dir := t.TempDir()

	gomod := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(gomod, []byte("moduleXYZ github.com/d0ugal/thrawn\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := ModulePath(gomod); got != "" {
		t.Errorf("moduleXYZ should not match the module directive, got %q", got)
	}
}

func TestModulePathMissingFile(t *testing.T) {
	if got := ModulePath(filepath.Join(t.TempDir(), "nae-such.mod")); got != "" {
		t.Errorf("missing go.mod should yield empty string, got %q", got)
	}
}

func TestModulePathNoModuleLine(t *testing.T) {
	dir := t.TempDir()

	gomod := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(gomod, []byte("// thrawn file, nae module line\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := ModulePath(gomod); got != "" {
		t.Errorf("go.mod without module line should yield empty, got %q", got)
	}
}

// End-to-end: parse two profiles and render, confirming module stripping and
// most-uncovered-first ordering hold through the whole pipeline.
func TestParseBuildRenderPipeline(t *testing.T) {
	head, err := Parse(strings.NewReader(brawProfile), module)
	if err != nil {
		t.Fatalf("Parse head: %v", err)
	}

	rows := BuildRows(head, nil)
	out := RenderTable(rows, false)
	// dreich (3 uncovered) must precede braw (0 uncovered) in the output.
	di := strings.Index(out, "dreich")

	bi := strings.Index(out, "braw")
	if di < 0 || bi < 0 || di > bi {
		t.Errorf("expected dreich before braw in table:\n%s", out)
	}
}
