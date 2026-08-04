// Command docsdiff renders row-aligned PNG diffs for docs-preview screenshots.
//
// It preserves the old workflow helper contracts:
//
//	docsdiff <base.png> <head.png> <out.png>
//	docsdiff <pages.json> <baseDir> <headDir> <outDir>
//
// The three-argument form exits 0 after writing a diff PNG, 3 when the renders
// are identical or only differ by denoised jitter, and 2 for usage errors. The
// four-argument form is the docs-preview batch driver: it writes a flat image
// directory plus manifest.json and prints a compact count summary.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultMaxD    = 1500
	defaultMinRun  = 4
	defaultPadding = 40
	defaultGutter  = 12
	defaultGap     = 20
)

var defaultViewports = []string{"desktop", "mobile"}

var errNoDiff = errors.New("no visual diff")

type rgbaImage struct {
	width  int
	height int
	data   []byte
}

type interval [2]int

type segment struct {
	base interval
	head interval
}

type hunk struct {
	base interval
	head interval
}

type page struct {
	Name      string   `json:"name"`
	HasBase   bool     `json:"hasBase"`
	Deleted   bool     `json:"deleted"`
	Viewports []string `json:"viewports,omitempty"`
}

type manifestPage struct {
	name    string
	entries []manifestEntry
}

type manifestEntry struct {
	viewport string
	kind     string
	file     string
}

type orderedCounts struct {
	order  []string
	counts map[string]int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	switch len(args) {
	case 3:
		//nolint:gosec // G602: len(args) is fixed by this switch case.
		err := runSingle(args[0], args[1], args[2])
		if err == nil {
			return 0
		}

		if errors.Is(err, errNoDiff) {
			return 3
		}

		_, _ = fmt.Fprintf(stderr, "docs-diff: %v\n", err)

		return 1
	case 4:
		//nolint:gosec // G602: len(args) is fixed by this switch case.
		if err := runBatch(args[0], args[1], args[2], args[3], stdout); err != nil {
			_, _ = fmt.Fprintf(stderr, "docs-diff-run: %v\n", err)
			return 1
		}

		return 0
	default:
		_, _ = fmt.Fprintln(stderr, "usage: docsdiff <base.png> <head.png> <out.png>")
		_, _ = fmt.Fprintln(stderr, "   or: docsdiff <pages.json> <baseDir> <headDir> <outDir>")

		return 2
	}
}

func runSingle(basePath, headPath, outPath string) error {
	base, err := readPNG(basePath)
	if err != nil {
		return err
	}

	head, err := readPNG(headPath)
	if err != nil {
		return err
	}

	segs := denoiseSegments(diffRows(hashRows(base), hashRows(head), defaultMaxD), defaultMinRun)
	if len(segs) == 0 {
		return errNoDiff
	}

	hunks := buildHunks(segs, base.height, head.height, defaultPadding)

	return writePNG(outPath, renderDiff(base, head, hunks, defaultGutter, defaultGap))
}

func runBatch(pagesPath, baseDir, headDir, outDir string, stdout io.Writer) error {
	//nolint:gosec // G703: this workflow helper intentionally reads caller-supplied local paths.
	data, err := os.ReadFile(pagesPath)
	if err != nil {
		return err
	}

	var pages []page
	if err := json.Unmarshal(data, &pages); err != nil {
		return err
	}

	//nolint:gosec // G301: match the old workflow artifact directory readability.
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	counts := newOrderedCounts()

	manifest := make([]manifestPage, 0, len(pages))
	for _, page := range pages {
		if !isSafeArtifactName(page.Name) {
			return fmt.Errorf("unsafe page name %q", page.Name)
		}

		manifestPage := manifestPage{name: page.Name}

		viewports, err := page.viewportLabels()
		if err != nil {
			return err
		}

		for _, viewport := range viewports {
			file := page.Name + "-" + viewport + ".png"
			basePath := filepath.Join(baseDir, file)
			headPath := filepath.Join(headDir, file)
			outPath := filepath.Join(outDir, file)

			if page.Deleted {
				if fileExists(basePath) {
					if err := copyFile(basePath, outPath); err != nil {
						return err
					}

					manifestPage.add(viewport, "deleted", file, counts)
				}

				continue
			}

			if !fileExists(headPath) {
				continue
			}

			hasBase := page.HasBase && fileExists(basePath)
			if !hasBase {
				if err := copyFile(headPath, outPath); err != nil {
					return err
				}

				manifestPage.add(viewport, "new", file, counts)

				continue
			}

			base, err := readPNG(basePath)
			if err != nil {
				return err
			}

			head, err := readPNG(headPath)
			if err != nil {
				return err
			}

			segs := denoiseSegments(diffRows(hashRows(base), hashRows(head), defaultMaxD), defaultMinRun)
			if len(segs) == 0 {
				manifestPage.add(viewport, "same", "", counts)
				continue
			}

			hunks := buildHunks(segs, base.height, head.height, defaultPadding)
			if err := writePNG(outPath, renderDiff(base, head, hunks, defaultGutter, defaultGap)); err != nil {
				return err
			}

			manifestPage.add(viewport, "diff", file, counts)
		}

		if len(manifestPage.entries) > 0 {
			manifest = append(manifest, manifestPage)
		}
	}

	//nolint:gosec // G306: generated preview artifacts should remain readable like Node's default writes.
	if err := os.WriteFile(filepath.Join(outDir, "manifest.json"), marshalManifest(manifest), 0o644); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "docs-diff: %s\n", counts.marshalJSON())

	return nil
}

func (p page) viewportLabels() ([]string, error) {
	if len(p.Viewports) == 0 {
		return defaultViewports, nil
	}

	labels := make([]string, 0, len(p.Viewports))
	for _, viewport := range p.Viewports {
		viewport = strings.TrimSpace(viewport)
		if viewport == "" {
			return nil, fmt.Errorf("page %q has an empty viewport label", p.Name)
		}

		if !isSafeArtifactName(viewport) {
			return nil, fmt.Errorf("page %q has an unsafe viewport label %q", p.Name, viewport)
		}

		labels = append(labels, viewport)
	}

	return labels, nil
}

func isSafeArtifactName(name string) bool {
	if name == "" || strings.Contains(name, "..") {
		return false
	}

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}

	return true
}

func (p *manifestPage) add(viewport, kind, file string, counts *orderedCounts) {
	p.entries = append(p.entries, manifestEntry{viewport: viewport, kind: kind, file: file})
	counts.add(kind)
}

func fileExists(path string) bool {
	//nolint:gosec // G703: this helper checks workflow-local screenshot paths supplied by the caller.
	_, err := os.Stat(path)
	return err == nil
}

func copyFile(src, dst string) error {
	//nolint:gosec // G703: this helper copies workflow-local screenshot paths supplied by the caller.
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	//nolint:gosec // G306: copied preview PNGs should remain readable like the old Node helper output.
	return os.WriteFile(dst, data, 0o644)
}

func readPNG(path string) (rgbaImage, error) {
	//nolint:gosec // G703: this helper intentionally opens caller-supplied local PNG paths.
	f, err := os.Open(path)
	if err != nil {
		return rgbaImage{}, err
	}
	defer func() { _ = f.Close() }()

	img, err := png.Decode(f)
	if err != nil {
		return rgbaImage{}, err
	}

	return imageToRGBA(img), nil
}

func imageToRGBA(img image.Image) rgbaImage {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	out := rgbaImage{
		width:  width,
		height: height,
		data:   make([]byte, width*height*4),
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			c := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			i := (y*width + x) * 4
			out.data[i] = c.R
			out.data[i+1] = c.G
			out.data[i+2] = c.B
			out.data[i+3] = c.A
		}
	}

	return out
}

func writePNG(path string, img rgbaImage) error {
	out := image.NRGBA{
		Pix:    img.data,
		Stride: img.width * 4,
		Rect:   image.Rect(0, 0, img.width, img.height),
	}

	//nolint:gosec // G703: this helper intentionally writes the caller-supplied local output path.
	f, err := os.Create(path)
	if err != nil {
		return err
	}

	defer func() { _ = f.Close() }()

	return png.Encode(f, &out)
}

func hashRow(data []byte, start, end int) string {
	h1 := uint32(0x811c9dc5)
	h2 := uint32(0xc59d1c81)

	for i := start; i < end; i++ {
		b := uint32(data[i])
		h1 = (h1 ^ b) * 0x01000193
		h2 = (h2 ^ b) * 0x85ebca77
	}

	return strconv.FormatUint(uint64(h1), 16) + strconv.FormatUint(uint64(h2), 16)
}

func hashRows(img rgbaImage) []string {
	stride := img.width * 4

	rows := make([]string, img.height)
	for y := 0; y < img.height; y++ {
		rows[y] = hashRow(img.data, y*stride, y*stride+stride)
	}

	return rows
}

func myersOps(a, b []string, maxD int) ([]byte, bool) {
	n := len(a)
	m := len(b)

	limit := n + m
	if limit == 0 {
		return []byte{}, true
	}

	capD := maxD
	if capD > limit {
		capD = limit
	}

	if capD < 0 {
		capD = limit
	}

	offset := capD + 1
	size := 2*capD + 3
	v := make([]int, size)
	trace := make([][]int, 0, capD+1)
	found := -1

	for d := 0; d <= capD; d++ {
		vd := make([]int, len(v))
		copy(vd, v)
		trace = append(trace, vd)

		for k := -d; k <= d; k += 2 {
			kIdx := k + offset

			var x int
			if k == -d || (k != d && v[kIdx-1] < v[kIdx+1]) {
				x = v[kIdx+1]
			} else {
				x = v[kIdx-1] + 1
			}

			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}

			v[kIdx] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}

		if found >= 0 {
			break
		}
	}

	if found < 0 {
		return nil, false
	}

	ops := make([]byte, 0, limit)
	x := n
	y := m

	for d := found; d > 0; d-- {
		vd := trace[d]
		k := x - y
		kIdx := k + offset

		var prevK int
		if k == -d || (k != d && vd[kIdx-1] < vd[kIdx+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}

		prevX := vd[prevK+offset]

		prevY := prevX - prevK
		for x > prevX && y > prevY {
			ops = append(ops, '=')
			x--
			y--
		}

		if x == prevX {
			ops = append(ops, 'i')
		} else {
			ops = append(ops, 'd')
		}

		x = prevX
		y = prevY
	}

	for x > 0 && y > 0 {
		ops = append(ops, '=')
		x--
		y--
	}

	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}

	return ops, true
}

func diffRows(base, head []string, maxD int) []segment {
	nb := len(base)
	nh := len(head)
	minN := min(nb, nh)

	prefix := 0
	for prefix < minN && base[prefix] == head[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < minN-prefix && base[nb-1-suffix] == head[nh-1-suffix] {
		suffix++
	}

	baseMid := base[prefix : nb-suffix]

	headMid := head[prefix : nh-suffix]
	if len(baseMid) == 0 && len(headMid) == 0 {
		return nil
	}

	ops, ok := myersOps(baseMid, headMid, maxD)
	if !ok {
		return []segment{{base: interval{prefix, nb - suffix}, head: interval{prefix, nh - suffix}}}
	}

	var segs []segment

	bi := prefix
	hi := prefix

	var cur segment

	active := false

	for _, op := range ops {
		switch op {
		case '=':
			if active {
				segs = append(segs, cur)
				active = false
			}

			bi++
			hi++
		case 'd':
			if !active {
				cur = segment{base: interval{bi, bi}, head: interval{hi, hi}}
				active = true
			}

			bi++
			cur.base[1] = bi
		case 'i':
			if !active {
				cur = segment{base: interval{bi, bi}, head: interval{hi, hi}}
				active = true
			}

			hi++
			cur.head[1] = hi
		}
	}

	if active {
		segs = append(segs, cur)
	}

	return segs
}

func denoiseSegments(segs []segment, minRun int) []segment {
	if minRun <= 0 {
		return segs
	}

	kept := make([]segment, 0, len(segs))
	for _, seg := range segs {
		baseLen := seg.base[1] - seg.base[0]

		headLen := seg.head[1] - seg.head[0]
		if max(baseLen, headLen) >= minRun {
			kept = append(kept, seg)
		}
	}

	return kept
}

func buildHunks(segs []segment, baseHeight, headHeight, padding int) []hunk {
	if len(segs) == 0 {
		return nil
	}

	expanded := make([]hunk, 0, len(segs))
	for _, seg := range segs {
		expanded = append(expanded, hunk{
			base: interval{max(0, seg.base[0]-padding), min(baseHeight, seg.base[1]+padding)},
			head: interval{max(0, seg.head[0]-padding), min(headHeight, seg.head[1]+padding)},
		})
	}

	sort.Slice(expanded, func(i, j int) bool {
		if expanded[i].head[0] != expanded[j].head[0] {
			return expanded[i].head[0] < expanded[j].head[0]
		}

		return expanded[i].base[0] < expanded[j].base[0]
	})

	merged := []hunk{expanded[0]}
	for _, h := range expanded[1:] {
		last := &merged[len(merged)-1]
		if h.head[0] <= last.head[1] || h.base[0] <= last.base[1] {
			last.head[0] = min(last.head[0], h.head[0])
			last.head[1] = max(last.head[1], h.head[1])
			last.base[0] = min(last.base[0], h.base[0])
			last.base[1] = max(last.base[1], h.base[1])

			continue
		}

		merged = append(merged, h)
	}

	return merged
}

func renderDiff(base, head rgbaImage, hunks []hunk, gutter, gap int) rgbaImage {
	colW := max(base.width, head.width)
	outW := colW*2 + gutter

	type band struct {
		base   interval
		head   interval
		height int
	}

	bands := make([]band, 0, len(hunks))
	outH := 0

	for _, h := range hunks {
		height := max(h.base[1]-h.base[0], h.head[1]-h.head[0])
		bands = append(bands, band{base: h.base, head: h.head, height: height})
		outH += height
	}

	if len(bands) > 1 {
		outH += gap * (len(bands) - 1)
	}

	if outH < 1 {
		outH = 1
	}

	out := rgbaImage{width: outW, height: outH, data: make([]byte, outW*outH*4)}
	for i := 0; i < len(out.data); i += 4 {
		out.data[i] = 0xe2
		out.data[i+1] = 0xe2
		out.data[i+2] = 0xe2
		out.data[i+3] = 0xff
	}

	blit := func(src rgbaImage, srcRow0, srcRow1, destX, destY, rows int) {
		srcStride := src.width * 4
		dstStride := out.width * 4
		copyBytes := min(src.width, colW) * 4

		for r := 0; r < rows; r++ {
			sr := srcRow0 + r
			if sr >= srcRow1 || sr >= src.height {
				break
			}

			srcStart := sr * srcStride
			dstStart := (destY+r)*dstStride + destX*4
			copy(out.data[dstStart:dstStart+copyBytes], src.data[srcStart:srcStart+copyBytes])
		}
	}

	y := 0
	for _, band := range bands {
		blit(base, band.base[0], band.base[1], 0, y, band.height)
		blit(head, band.head[0], band.head[1], colW+gutter, y, band.height)
		y += band.height + gap
	}

	return out
}

func marshalManifest(pages []manifestPage) []byte {
	if len(pages) == 0 {
		return []byte("{}")
	}

	var buf bytes.Buffer
	buf.WriteString("{\n")

	for pageIndex, page := range pages {
		if pageIndex > 0 {
			buf.WriteString(",\n")
		}

		buf.WriteString("  ")
		writeJSONString(&buf, page.name)
		buf.WriteString(": {\n")

		for entryIndex, entry := range page.entries {
			if entryIndex > 0 {
				buf.WriteString(",\n")
			}

			buf.WriteString("    ")
			writeJSONString(&buf, entry.viewport)
			buf.WriteString(": {\n")
			buf.WriteString("      \"kind\": ")
			writeJSONString(&buf, entry.kind)

			if entry.file != "" {
				buf.WriteString(",\n      \"file\": ")
				writeJSONString(&buf, entry.file)
				buf.WriteString("\n    }")
			} else {
				buf.WriteString("\n    }")
			}
		}

		buf.WriteString("\n  }")
	}

	buf.WriteString("\n}")

	return buf.Bytes()
}

func newOrderedCounts() *orderedCounts {
	return &orderedCounts{counts: map[string]int{}}
}

func (c *orderedCounts) add(kind string) {
	if _, ok := c.counts[kind]; !ok {
		c.order = append(c.order, kind)
	}

	c.counts[kind]++
}

func (c *orderedCounts) marshalJSON() []byte {
	if len(c.order) == 0 {
		return []byte("{}")
	}

	var buf bytes.Buffer
	buf.WriteByte('{')

	for i, kind := range c.order {
		if i > 0 {
			buf.WriteByte(',')
		}

		writeJSONString(&buf, kind)
		buf.WriteByte(':')
		buf.WriteString(strconv.Itoa(c.counts[kind]))
	}

	buf.WriteByte('}')

	return buf.Bytes()
}

func writeJSONString(buf *bytes.Buffer, value string) {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}

	buf.Write(encoded)
}
