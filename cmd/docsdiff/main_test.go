package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHashRowsMatchesLegacyTwoLaneValues(t *testing.T) {
	t.Parallel()

	rows := hashRows(rowImage([]byte{1, 1, 2, 3, 3}, 2))

	want := []string{
		"9831a510968341",
		"9831a510968341",
		"b2deca1d809be629",
		"ba0d6f452a72f881",
		"ba0d6f452a72f881",
	}
	if strings.Join(rows, ",") != strings.Join(want, ",") {
		t.Fatalf("hashRows() = %#v, want %#v", rows, want)
	}
}

func TestDiffRowsAlignsCommonScreenshotEdits(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base     []byte
		head     []byte
		maxD     int
		wantSegs []segment
	}{
		"braw identical page": {
			base:     []byte{1, 2, 3, 4, 5},
			head:     []byte{1, 2, 3, 4, 5},
			wantSegs: nil,
		},
		"canny mid page insertion realigns tail": {
			base: []byte{1, 2, 3, 4, 5, 6},
			head: []byte{1, 2, 3, 91, 92, 4, 5, 6},
			wantSegs: []segment{
				{base: interval{3, 3}, head: interval{3, 5}},
			},
		},
		"thrawn pure deletion has empty head range": {
			base: []byte{1, 2, 3, 4, 5},
			head: []byte{1, 2, 5},
			wantSegs: []segment{
				{base: interval{2, 4}, head: interval{2, 2}},
			},
		},
		"blether disjoint replacements stay separate": {
			base: []byte{1, 2, 3, 4, 5, 6, 7, 8},
			head: []byte{1, 99, 3, 4, 5, 6, 88, 8},
			wantSegs: []segment{
				{base: interval{1, 2}, head: interval{1, 2}},
				{base: interval{6, 7}, head: interval{6, 7}},
			},
		},
		"dreich global divergence falls back to one segment": {
			base: []byte{1, 2, 3, 4},
			head: []byte{5, 6, 7, 8},
			maxD: 2,
			wantSegs: []segment{
				{base: interval{0, 4}, head: interval{0, 4}},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			maxD := test.maxD
			if maxD == 0 {
				maxD = defaultMaxD
			}

			got := diffRows(hashRows(rowImage(test.base, 2)), hashRows(rowImage(test.head, 2)), maxD)
			assertSegments(t, got, test.wantSegs)
		})
	}
}

func TestMyersOpsReportsDivergencePastDifferenceCap(t *testing.T) {
	t.Parallel()

	base := hashRows(rowImage([]byte{1, 2, 3, 4}, 2))

	head := hashRows(rowImage([]byte{5, 6, 7, 8}, 2))
	if _, ok := myersOps(base, head, 2); ok {
		t.Fatal("myersOps() ok = true, want false for dreich global change past cap")
	}
}

func TestDenoiseSegmentsDropsSmallReflowJitter(t *testing.T) {
	t.Parallel()

	segs := []segment{
		{base: interval{100, 140}, head: interval{100, 180}},
		{base: interval{900, 901}, head: interval{900, 902}},
		{base: interval{1500, 1502}, head: interval{1568, 1568}},
	}
	got := denoiseSegments(segs, 4)
	want := []segment{{base: interval{100, 140}, head: interval{100, 180}}}
	assertSegments(t, got, want)

	kept := denoiseSegments([]segment{{base: interval{50, 50}, head: interval{50, 90}}}, 4)
	if len(kept) != 1 {
		t.Fatalf("pure insertion denoised away, want kept")
	}

	if got := denoiseSegments(segs, 0); len(got) != len(segs) {
		t.Fatalf("minRun 0 kept %d segments, want %d", len(got), len(segs))
	}
}

func TestBuildHunksPadsClampsAndMerges(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		segs       []segment
		baseH      int
		headH      int
		padding    int
		wantHunks  []hunk
		wantLength int
	}{
		"canny segment pads within bounds": {
			segs: []segment{
				{base: interval{50, 52}, head: interval{50, 54}},
			},
			baseH:   100,
			headH:   100,
			padding: 10,
			wantHunks: []hunk{
				{base: interval{40, 62}, head: interval{40, 64}},
			},
		},
		"bairn top edge clamps to zero": {
			segs: []segment{
				{base: interval{2, 3}, head: interval{2, 3}},
			},
			baseH:   100,
			headH:   100,
			padding: 40,
			wantHunks: []hunk{
				{base: interval{0, 43}, head: interval{0, 43}},
			},
		},
		"strath nearby padded edits merge": {
			segs: []segment{
				{base: interval{10, 11}, head: interval{10, 11}},
				{base: interval{30, 31}, head: interval{30, 31}},
			},
			baseH:   200,
			headH:   200,
			padding: 15,
			wantHunks: []hunk{
				{base: interval{0, 46}, head: interval{0, 46}},
			},
		},
		"croft distant edits stay separate": {
			segs: []segment{
				{base: interval{10, 11}, head: interval{10, 11}},
				{base: interval{100, 101}, head: interval{100, 101}},
			},
			baseH:      300,
			headH:      300,
			padding:    10,
			wantLength: 2,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := buildHunks(test.segs, test.baseH, test.headH, test.padding)
			if test.wantLength != 0 && len(got) != test.wantLength {
				t.Fatalf("buildHunks() length = %d, want %d: %#v", len(got), test.wantLength, got)
			}

			if test.wantHunks != nil {
				assertHunks(t, got, test.wantHunks)
			}
		})
	}

	if got := buildHunks(nil, 10, 10, 40); len(got) != 0 {
		t.Fatalf("buildHunks(nil) length = %d, want 0", len(got))
	}
}

func TestRenderDiffCompositeGeometryAndPixels(t *testing.T) {
	t.Parallel()

	base := alternatingImage(30, 4, 100)
	head := alternatingImage(30, 4, 200)
	out := renderDiff(base, head, []hunk{{base: interval{5, 9}, head: interval{5, 9}}}, 12, 20)

	if out.width != 20 {
		t.Fatalf("renderDiff width = %d, want 20", out.width)
	}

	if out.height != 4 {
		t.Fatalf("renderDiff height = %d, want 4", out.height)
	}

	assertPixel(t, out, 0, 0, [4]byte{100, 100, 100, 255})
	assertPixel(t, out, 16, 0, [4]byte{200, 200, 200, 255})
	assertPixel(t, out, 5, 0, [4]byte{0xe2, 0xe2, 0xe2, 0xff})

	shortBase := solidImage(10, 3, 50)
	tallHead := solidImage(10, 3, 60)

	padded := renderDiff(shortBase, tallHead, []hunk{{base: interval{0, 2}, head: interval{0, 5}}}, 4, 0)
	if padded.height != 5 {
		t.Fatalf("short-column composite height = %d, want 5", padded.height)
	}

	assertPixel(t, padded, 0, 3, [4]byte{0xe2, 0xe2, 0xe2, 0xff})
}

func TestRunSinglePreservesExitBehavior(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	basePath := filepath.Join(dir, "canny-base.png")
	headPath := filepath.Join(dir, "canny-head.png")
	outPath := filepath.Join(dir, "canny-out.png")
	base := rowImage([]byte{1, 2, 3, 4, 5, 6}, 3)
	writePNGForTest(t, basePath, base)
	writePNGForTest(t, headPath, base)

	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)
	if got := run([]string{basePath, headPath, outPath}, &stdout, &stderr); got != 3 {
		t.Fatalf("identical run exit = %d, want 3; stderr=%q", got, stderr.String())
	}

	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("identical run wrote output, stat error = %v", err)
	}

	changed := rowImage([]byte{1, 2, 90, 91, 92, 93, 5, 6}, 3)
	writePNGForTest(t, headPath, changed)

	if got := run([]string{basePath, headPath, outPath}, &stdout, &stderr); got != 0 {
		t.Fatalf("changed run exit = %d, want 0; stderr=%q", got, stderr.String())
	}

	diff := readPNGForTest(t, outPath)
	if diff.width != 18 {
		t.Fatalf("single diff width = %d, want 18", diff.width)
	}

	if got := run([]string{"only-one-arg"}, &stdout, &stderr); got != 2 {
		t.Fatalf("usage exit = %d, want 2", got)
	}
}

func TestRunBatchPreservesManifestCountsAndPageKinds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	baseDir := filepath.Join(dir, "base")
	headDir := filepath.Join(dir, "head")
	outDir := filepath.Join(dir, "out")

	for _, path := range []string{baseDir, headDir} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}

	pagesJSON := `[
  {"url":"/docs/canny/","name":"canny","hasBase":true,"deleted":false},
  {"url":"/docs/braw/","name":"braw","hasBase":false,"deleted":false},
  {"url":"/docs/dreich/","name":"dreich","hasBase":true,"deleted":true},
  {"url":"/docs/blether/","name":"blether","hasBase":true,"deleted":false},
  {"url":"/docs/strath/","name":"strath","hasBase":true,"deleted":false}
]`

	pagesPath := filepath.Join(dir, "pages.json")
	if err := os.WriteFile(pagesPath, []byte(pagesJSON), 0o600); err != nil {
		t.Fatalf("write pages.json: %v", err)
	}

	cannyBase := rowImage([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 3)
	cannyHead := rowImage([]byte{1, 2, 90, 91, 92, 93, 7, 8}, 3)

	writePNGForTest(t, filepath.Join(baseDir, "canny-desktop.png"), cannyBase)
	writePNGForTest(t, filepath.Join(headDir, "canny-desktop.png"), cannyHead)
	writePNGForTest(t, filepath.Join(baseDir, "canny-mobile.png"), cannyBase)
	writePNGForTest(t, filepath.Join(headDir, "canny-mobile.png"), cannyBase)

	brawDesktop := solidImage(5, 2, 33)
	brawMobile := solidImage(4, 2, 44)

	writePNGForTest(t, filepath.Join(headDir, "braw-desktop.png"), brawDesktop)
	writePNGForTest(t, filepath.Join(headDir, "braw-mobile.png"), brawMobile)

	dreichDesktop := solidImage(6, 2, 77)
	writePNGForTest(t, filepath.Join(baseDir, "dreich-desktop.png"), dreichDesktop)

	bletherDesktop := solidImage(5, 2, 88)
	writePNGForTest(t, filepath.Join(headDir, "blether-desktop.png"), bletherDesktop)

	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)
	if got := run([]string{pagesPath, baseDir, headDir, outDir}, &stdout, &stderr); got != 0 {
		t.Fatalf("batch run exit = %d, want 0; stderr=%q", got, stderr.String())
	}

	if got, want := stdout.String(), "docs-diff: {\"diff\":1,\"same\":1,\"new\":3,\"deleted\":1}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	manifestData, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	wantManifest := `{
  "canny": {
    "desktop": {
      "kind": "diff",
      "file": "canny-desktop.png"
    },
    "mobile": {
      "kind": "same"
    }
  },
  "braw": {
    "desktop": {
      "kind": "new",
      "file": "braw-desktop.png"
    },
    "mobile": {
      "kind": "new",
      "file": "braw-mobile.png"
    }
  },
  "dreich": {
    "desktop": {
      "kind": "deleted",
      "file": "dreich-desktop.png"
    }
  },
  "blether": {
    "desktop": {
      "kind": "new",
      "file": "blether-desktop.png"
    }
  }
}`
	if string(manifestData) != wantManifest {
		t.Fatalf("manifest mismatch:\ngot:\n%s\nwant:\n%s", manifestData, wantManifest)
	}

	assertCopiedFile(t, filepath.Join(headDir, "braw-desktop.png"), filepath.Join(outDir, "braw-desktop.png"))
	assertCopiedFile(t, filepath.Join(baseDir, "dreich-desktop.png"), filepath.Join(outDir, "dreich-desktop.png"))
	assertCopiedFile(t, filepath.Join(headDir, "blether-desktop.png"), filepath.Join(outDir, "blether-desktop.png"))

	if _, err := os.Stat(filepath.Join(outDir, "canny-mobile.png")); !os.IsNotExist(err) {
		t.Fatalf("same page wrote image, stat error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "strath-desktop.png")); !os.IsNotExist(err) {
		t.Fatalf("unshot page wrote image, stat error = %v", err)
	}

	composite := readPNGForTest(t, filepath.Join(outDir, "canny-desktop.png"))
	if composite.width != 18 {
		t.Fatalf("batch composite width = %d, want 18", composite.width)
	}

	if composite.height != 8 {
		t.Fatalf("batch composite height = %d, want 8", composite.height)
	}

	assertPixel(t, composite, 0, 2, [4]byte{3, 3, 3, 255})
	assertPixel(t, composite, 15, 2, [4]byte{90, 90, 90, 255})
	assertPixel(t, composite, 4, 2, [4]byte{0xe2, 0xe2, 0xe2, 0xff})
}

func TestRunBatchUsesCustomViewportLabels(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	baseDir := filepath.Join(dir, "base")
	headDir := filepath.Join(dir, "head")
	outDir := filepath.Join(dir, "out")

	for _, path := range []string{baseDir, headDir} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}

	pagesJSON := `[
  {"name":"canny","hasBase":true,"deleted":false,"viewports":["small","wide"]}
]`

	pagesPath := filepath.Join(dir, "pages.json")
	if err := os.WriteFile(pagesPath, []byte(pagesJSON), 0o600); err != nil {
		t.Fatalf("write pages.json: %v", err)
	}

	base := rowImage([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 2)
	head := rowImage([]byte{1, 2, 90, 91, 92, 93, 7, 8}, 2)

	writePNGForTest(t, filepath.Join(baseDir, "canny-small.png"), base)
	writePNGForTest(t, filepath.Join(headDir, "canny-small.png"), base)
	writePNGForTest(t, filepath.Join(baseDir, "canny-wide.png"), base)
	writePNGForTest(t, filepath.Join(headDir, "canny-wide.png"), head)

	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)
	if got := run([]string{pagesPath, baseDir, headDir, outDir}, &stdout, &stderr); got != 0 {
		t.Fatalf("batch run exit = %d, want 0; stderr=%q", got, stderr.String())
	}

	if got, want := stdout.String(), "docs-diff: {\"same\":1,\"diff\":1}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	manifestData, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	wantManifest := `{
  "canny": {
    "small": {
      "kind": "same"
    },
    "wide": {
      "kind": "diff",
      "file": "canny-wide.png"
    }
  }
}`
	if string(manifestData) != wantManifest {
		t.Fatalf("manifest mismatch:\ngot:\n%s\nwant:\n%s", manifestData, wantManifest)
	}

	if _, err := os.Stat(filepath.Join(outDir, "canny-small.png")); !os.IsNotExist(err) {
		t.Fatalf("same custom viewport wrote image, stat error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "canny-wide.png")); err != nil {
		t.Fatalf("missing diff image: %v", err)
	}
}

func TestRunBatchRejectsUnsafeNames(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		pagesJSON string
		want      string
	}{
		"canny viewport": {
			pagesJSON: `[{"name":"canny","viewports":["../wide"]}]`,
			want:      "unsafe viewport label",
		},
		"dreich page": {
			pagesJSON: `[{"name":"../canny","viewports":["wide"]}]`,
			want:      "unsafe page name",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			pagesPath := filepath.Join(dir, "pages.json")
			if err := os.WriteFile(pagesPath, []byte(test.pagesJSON), 0o600); err != nil {
				t.Fatalf("write pages.json: %v", err)
			}

			var (
				stdout bytes.Buffer
				stderr bytes.Buffer
			)
			if got := run([]string{pagesPath, dir, dir, filepath.Join(dir, "out")}, &stdout, &stderr); got == 0 {
				t.Fatalf("batch run exit = 0, want failure; stdout=%q", stdout.String())
			}

			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

func assertSegments(t *testing.T, got, want []segment) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("segments = %#v, want %#v", got, want)
	}
}

func assertHunks(t *testing.T, got, want []hunk) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("hunks = %#v, want %#v", got, want)
	}
}

func rowImage(rows []byte, width int) rgbaImage {
	img := rgbaImage{
		width:  width,
		height: len(rows),
		data:   make([]byte, width*len(rows)*4),
	}
	for y, value := range rows {
		for x := 0; x < width; x++ {
			i := (y*width + x) * 4
			img.data[i] = value
			img.data[i+1] = value
			img.data[i+2] = value
			img.data[i+3] = 255
		}
	}

	return img
}

func solidImage(height, width int, value byte) rgbaImage {
	rows := bytes.Repeat([]byte{value}, height)
	return rowImage(rows, width)
}

func alternatingImage(height, width int, oddValue byte) rgbaImage {
	rows := make([]byte, height)
	for i := range rows {
		if i%2 == 1 {
			rows[i] = oddValue
		}
	}

	return rowImage(rows, width)
}

func assertPixel(t *testing.T, img rgbaImage, x, y int, want [4]byte) {
	t.Helper()

	i := (y*img.width + x) * 4

	got := [4]byte{img.data[i], img.data[i+1], img.data[i+2], img.data[i+3]}
	if got != want {
		t.Fatalf("pixel(%d,%d) = %#v, want %#v", x, y, got, want)
	}
}

func writePNGForTest(t *testing.T, path string, img rgbaImage) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	nrgba := image.NRGBA{
		Pix:    img.data,
		Stride: img.width * 4,
		Rect:   image.Rect(0, 0, img.width, img.height),
	}
	if err := png.Encode(f, &nrgba); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}

func readPNGForTest(t *testing.T, path string) rgbaImage {
	t.Helper()

	img, err := readPNG(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return img
}

func assertCopiedFile(t *testing.T, wantPath, gotPath string) {
	t.Helper()

	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read want %s: %v", wantPath, err)
	}

	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("read got %s: %v", gotPath, err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("%s does not match copied source %s", gotPath, wantPath)
	}
}
