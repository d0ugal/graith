//go:build !libghostty || libghostty_compare

package pty

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var terminalBenchmarkSink uint64

const (
	terminalBenchmarkScrollCols  = 240
	terminalBenchmarkScrollRows  = 40
	terminalBenchmarkScrollBatch = 256
)

// terminalBackendFactory lets the same reduced synthetic corpus and benchmarks
// exercise the rollback backend and an explicitly tagged native candidate.
type terminalBackendFactory struct {
	name          string
	new           func(cols, rows int) (Terminal, error)
	expectations  terminalBackendExpectations
	helperCPUTime func(Terminal) (time.Duration, bool)
	helperPID     func(Terminal) (int, bool)
}

func terminalBackendFactories() []terminalBackendFactory {
	backends := []terminalBackendFactory{
		{
			name: "charm",
			new: func(cols, rows int) (Terminal, error) {
				return newCharmTerminal(cols, rows), nil
			},
			expectations: charmTerminalBackendExpectations(),
		},
	}

	return append(backends, nativeTerminalBackendFactories()...)
}

func newTerminalBackendTestTerm(
	t testing.TB,
	factory terminalBackendFactory,
	cols, rows int,
) Terminal {
	t.Helper()

	term, err := factory.new(cols, rows)
	if err != nil {
		t.Fatalf("new %s terminal: %v", factory.name, err)
	}

	t.Cleanup(func() {
		if err := term.Close(); err != nil {
			t.Errorf("close %s terminal: %v", factory.name, err)
		}
	})

	return term
}

func BenchmarkTerminalBackends(b *testing.B) {
	parseWorkload := syntheticTerminalWorkload(64 * 1024)
	reconstructionWorkload := syntheticTerminalWorkload(4 * 1024 * 1024)
	dirtyMutations := [...]struct {
		input   []byte
		content string
	}{
		{input: []byte("\x1b[H\x1b[31mb"), content: "b"},
		{input: []byte("\x1b[H\x1b[32mc"), content: "c"},
	}

	for _, factory := range terminalBackendFactories() {
		b.Run(factory.name, func(b *testing.B) {
			b.Run("start_close", func(b *testing.B) {
				var (
					checksum          = benchmarkFNVOffset
					helperCPU         time.Duration
					measuredHelperCPU bool
				)

				b.ReportAllocs()
				parentCPU, parentCPUOK := startTerminalBenchmark(b)

				for range b.N {
					term, err := factory.new(120, 40)
					if err != nil {
						b.Fatal(err)
					}

					cols, rows := term.Size()
					checksum = benchmarkChecksumInt(checksum, cols)
					checksum = benchmarkChecksumInt(checksum, rows)

					if err := term.Close(); err != nil {
						b.Fatal(err)
					}

					if cpu, ok := terminalHelperCPU(factory, term); ok {
						helperCPU += cpu
						measuredHelperCPU = true
					}
				}

				parentCPUEnd, parentCPUEndOK := stopTerminalBenchmark(b)
				consumeTerminalBenchmarkChecksum(b, checksum)
				reportTerminalBenchmarkCPU(
					b, parentCPU, parentCPUOK, parentCPUEnd, parentCPUEndOK,
					helperCPU, measuredHelperCPU,
				)
			})

			b.Run("parse", func(b *testing.B) {
				term := newTerminalBackendTestTerm(b, factory, 120, 40)
				b.ReportAllocs()
				b.SetBytes(int64(len(parseWorkload)))
				parentCPU, parentCPUOK := startTerminalBenchmark(b)

				for range b.N {
					n, err := term.Write(parseWorkload)
					if err != nil {
						b.Fatal(err)
					}
					if n != len(parseWorkload) {
						b.Fatalf("parsed %d bytes, want %d", n, len(parseWorkload))
					}
				}

				parentCPUEnd, parentCPUEndOK := stopTerminalBenchmark(b)
				snapshot, err := snapshotTerminal(term)
				if err != nil {
					b.Fatal(err)
				}

				validateTerminalBenchmarkSnapshot(b, snapshot, 120, 40)
				if err := term.Close(); err != nil {
					b.Fatal(err)
				}

				helperCPU, measuredHelperCPU := terminalHelperCPU(factory, term)
				reportTerminalBenchmarkCPU(
					b, parentCPU, parentCPUOK, parentCPUEnd, parentCPUEndOK,
					helperCPU, measuredHelperCPU,
				)
			})

			b.Run("dirty_snapshot_120x40", func(b *testing.B) {
				term := newTerminalBackendTestTerm(b, factory, 120, 40)
				if _, err := term.Write(parseWorkload); err != nil {
					b.Fatal(err)
				}

				var snapshot TerminalSnapshot

				b.ReportAllocs()
				parentCPU, parentCPUOK := startTerminalBenchmark(b)

				for i := 0; i < b.N; i++ {
					mutation := dirtyMutations[i%len(dirtyMutations)]
					n, err := term.Write(mutation.input)
					if err != nil {
						b.Fatal(err)
					}
					if n != len(mutation.input) {
						b.Fatalf("wrote %d dirtying bytes, want %d", n, len(mutation.input))
					}

					snapshot, err = snapshotTerminal(term)
					if err != nil {
						b.Fatal(err)
					}
					if len(snapshot.Cells) == 0 {
						b.Fatal("dirty snapshot contained no cells")
					}
					if snapshot.Cells[0].Content != mutation.content {
						b.Fatalf("dirty snapshot cell = %q, want %q", snapshot.Cells[0].Content, mutation.content)
					}
				}

				parentCPUEnd, parentCPUEndOK := stopTerminalBenchmark(b)
				validateTerminalBenchmarkSnapshot(b, snapshot, 120, 40)
				reportTerminalBenchmarkCPU(
					b, parentCPU, parentCPUOK, parentCPUEnd, parentCPUEndOK, 0, false,
				)
			})

			b.Run("resize", func(b *testing.B) {
				term := newTerminalBackendTestTerm(b, factory, 80, 24)
				if _, err := term.Write(parseWorkload); err != nil {
					b.Fatal(err)
				}

				b.ReportAllocs()
				parentCPU, parentCPUOK := startTerminalBenchmark(b)

				for i := 0; i < b.N; i++ {
					if i%2 == 0 {
						if err := term.Resize(120, 40); err != nil {
							b.Fatal(err)
						}
					} else if err := term.Resize(80, 24); err != nil {
						b.Fatal(err)
					}
				}

				parentCPUEnd, parentCPUEndOK := stopTerminalBenchmark(b)
				wantCols, wantRows := 80, 24
				if b.N%2 == 1 {
					wantCols, wantRows = 120, 40
				}
				if cols, rows := term.Size(); cols != wantCols || rows != wantRows {
					b.Fatalf("resized geometry = %dx%d, want %dx%d", cols, rows, wantCols, wantRows)
				}

				snapshot, err := snapshotTerminal(term)
				if err != nil {
					b.Fatal(err)
				}

				validateTerminalBenchmarkSnapshot(b, snapshot, wantCols, wantRows)
				reportTerminalBenchmarkCPU(
					b, parentCPU, parentCPUOK, parentCPUEnd, parentCPUEndOK, 0, false,
				)
			})

			b.Run("reconstruct_4MiB", func(b *testing.B) {
				var (
					snapshot          TerminalSnapshot
					helperCPU         time.Duration
					measuredHelperCPU bool
				)

				b.ReportAllocs()
				b.SetBytes(int64(len(reconstructionWorkload)))
				parentCPU, parentCPUOK := startTerminalBenchmark(b)

				for range b.N {
					term, err := factory.new(120, 40)
					if err != nil {
						b.Fatal(err)
					}
					if err := writeTerminalChunks(term, reconstructionWorkload); err != nil {
						_ = term.Close()

						b.Fatal(err)
					}

					snapshot, err = snapshotTerminal(term)
					if err != nil {
						_ = term.Close()

						b.Fatal(err)
					}
					if err := term.Close(); err != nil {
						b.Fatal(err)
					}

					if cpu, ok := terminalHelperCPU(factory, term); ok {
						helperCPU += cpu
						measuredHelperCPU = true
					}
				}

				parentCPUEnd, parentCPUEndOK := stopTerminalBenchmark(b)
				validateTerminalBenchmarkSnapshot(b, snapshot, 120, 40)
				reportTerminalBenchmarkCPU(
					b, parentCPU, parentCPUOK, parentCPUEnd, parentCPUEndOK,
					helperCPU, measuredHelperCPU,
				)
			})
		})
	}
}

func startTerminalBenchmark(b *testing.B) (time.Duration, bool) {
	b.Helper()
	b.StopTimer()
	b.ResetTimer()

	parentCPU, _, ok := terminalBenchmarkProcessUsage()

	b.StartTimer()

	return parentCPU, ok
}

func stopTerminalBenchmark(b *testing.B) (time.Duration, bool) {
	b.Helper()
	b.StopTimer()

	parentCPU, _, ok := terminalBenchmarkProcessUsage()

	return parentCPU, ok
}

func reportTerminalBenchmarkCPU(
	b *testing.B,
	parentStart time.Duration,
	parentStartOK bool,
	parentEnd time.Duration,
	parentEndOK bool,
	helperCPU time.Duration,
	helperCPUOK bool,
) {
	b.Helper()

	if parentStartOK && parentEndOK && parentEnd >= parentStart {
		b.ReportMetric(float64(parentEnd-parentStart)/float64(b.N), "parent-cpu-ns/op")
	}
	if helperCPUOK {
		b.ReportMetric(float64(helperCPU)/float64(b.N), "helper-lifetime-cpu-ns/op")
	}
}

func terminalHelperCPU(factory terminalBackendFactory, term Terminal) (time.Duration, bool) {
	if factory.helperCPUTime == nil {
		return 0, false
	}

	return factory.helperCPUTime(term)
}

func terminalHelperPID(factory terminalBackendFactory, term Terminal) (int, bool) {
	if factory.helperPID == nil {
		return 0, false
	}

	return factory.helperPID(term)
}

func validateTerminalBenchmarkSnapshot(
	t testing.TB,
	snapshot TerminalSnapshot,
	wantCols, wantRows int,
) {
	t.Helper()

	if snapshot.Cols != wantCols || snapshot.Rows != wantRows {
		t.Fatalf("snapshot geometry = %dx%d, want %dx%d", snapshot.Cols, snapshot.Rows, wantCols, wantRows)
	}
	if len(snapshot.Cells) != wantCols*wantRows {
		t.Fatalf("snapshot cells = %d, want %d", len(snapshot.Cells), wantCols*wantRows)
	}

	consumeTerminalBenchmarkChecksum(t, terminalSnapshotChecksum(snapshot))
}

func consumeTerminalBenchmarkChecksum(t testing.TB, checksum uint64) {
	t.Helper()

	if checksum == 0 {
		t.Fatal("empty terminal benchmark checksum")
	}

	terminalBenchmarkSink = checksum
}

const (
	benchmarkFNVOffset = uint64(14695981039346656037)
	benchmarkFNVPrime  = uint64(1099511628211)
)

func terminalSnapshotChecksum(snapshot TerminalSnapshot) uint64 {
	checksum := benchmarkFNVOffset
	checksum = benchmarkChecksumInt(checksum, snapshot.Cols)
	checksum = benchmarkChecksumInt(checksum, snapshot.Rows)
	checksum = benchmarkChecksumInt(checksum, snapshot.CursorX)
	checksum = benchmarkChecksumInt(checksum, snapshot.CursorY)
	checksum = benchmarkChecksumBool(checksum, snapshot.CursorVisible)
	checksum = benchmarkChecksumInt(checksum, len(snapshot.Cells))

	for _, cell := range snapshot.Cells {
		checksum = benchmarkChecksumInt(checksum, len(cell.Content))
		for i := 0; i < len(cell.Content); i++ {
			checksum = benchmarkChecksumByte(checksum, cell.Content[i])
		}

		checksum = benchmarkChecksumByte(checksum, byte(cell.Style.FG.Kind))
		checksum = benchmarkChecksumUint64(checksum, uint64(cell.Style.FG.Value))
		checksum = benchmarkChecksumByte(checksum, byte(cell.Style.BG.Kind))
		checksum = benchmarkChecksumUint64(checksum, uint64(cell.Style.BG.Value))
		checksum = benchmarkChecksumBool(checksum, cell.Style.Bold)
		checksum = benchmarkChecksumBool(checksum, cell.Style.Faint)
		checksum = benchmarkChecksumBool(checksum, cell.Style.Italic)
		checksum = benchmarkChecksumBool(checksum, cell.Style.Underline)
		checksum = benchmarkChecksumBool(checksum, cell.Style.Blink)
		checksum = benchmarkChecksumBool(checksum, cell.Style.Reverse)
		checksum = benchmarkChecksumBool(checksum, cell.Style.Strikethrough)
	}

	return checksum
}

func benchmarkChecksumInt(checksum uint64, value int) uint64 {
	// The checksum intentionally preserves the machine-width two's-complement
	// representation; snapshot dimensions and cursor coordinates are bounded.
	return benchmarkChecksumUint64(checksum, uint64(value)) //nolint:gosec
}

func benchmarkChecksumUint64(checksum, value uint64) uint64 {
	for range 8 {
		checksum = benchmarkChecksumByte(checksum, byte(value))
		value >>= 8
	}

	return checksum
}

func benchmarkChecksumBool(checksum uint64, value bool) uint64 {
	if value {
		return benchmarkChecksumByte(checksum, 1)
	}

	return benchmarkChecksumByte(checksum, 0)
}

func benchmarkChecksumByte(checksum uint64, value byte) uint64 {
	return (checksum ^ uint64(value)) * benchmarkFNVPrime
}

// TestTerminalBackendPeakMemoryWorkload is an opt-in process-level workload.
// It records simultaneous parent/helper current-RSS checkpoints while all
// terminals are live, then records separate process-lifetime peaks after helper
// reap. Every backend/scenario/sample runs in a fresh test process.
func TestTerminalBackendPeakMemoryWorkload(t *testing.T) {
	name := os.Getenv("GRAITH_TERMINAL_MEMORY_BACKEND")
	if name == "" {
		t.Skip("set GRAITH_TERMINAL_MEMORY_BACKEND to run the process-level RSS workload")
	}

	scenarioName := os.Getenv("GRAITH_TERMINAL_MEMORY_WORKLOAD")
	if scenarioName == "" {
		t.Skip("set GRAITH_TERMINAL_MEMORY_WORKLOAD to run the process-level RSS workload")
	}

	var selected *terminalBackendFactory

	for _, factory := range terminalBackendFactories() {
		if factory.name == name {
			factory := factory
			selected = &factory

			break
		}
	}
	if selected == nil {
		t.Fatalf("unknown terminal backend %q", name)
	}

	scenario := terminalMemoryScenarioForName(t, scenarioName)
	terms := make([]Terminal, 0, scenario.terminals)
	closed := false

	t.Cleanup(func() {
		if closed {
			return
		}

		for _, term := range terms {
			_ = term.Close()
		}
	})

	for range scenario.terminals {
		term, err := selected.new(scenario.cols, scenario.rows)
		if err != nil {
			t.Fatalf("new %s terminal: %v", selected.name, err)
		}

		terms = append(terms, term)
	}

	rssSampler := newTerminalRSSSampler(*selected, terms)
	rssSampler.sample(t)
	fixtureBytes := runTerminalMemoryScenario(t, scenario, terms, func() {
		rssSampler.sample(t)
	})
	checksum := benchmarkFNVOffset

	for _, term := range terms {
		snapshot, err := snapshotTerminal(term)
		if err != nil {
			t.Fatal(err)
		}

		validateTerminalBenchmarkSnapshot(t, snapshot, scenario.cols, scenario.rows)
		checksum = benchmarkChecksumUint64(checksum, terminalSnapshotChecksum(snapshot))
	}

	consumeTerminalBenchmarkChecksum(t, checksum)
	runtime.GC()
	rssSampler.sample(t)
	rssSampler.report(t)

	for _, term := range terms {
		if err := term.Close(); err != nil {
			t.Fatal(err)
		}
	}

	closed = true
	reportTerminalPeakRSS(t, terms)

	var (
		helperCPU         time.Duration
		measuredHelperCPU bool
	)

	for _, term := range terms {
		if cpu, ok := terminalHelperCPU(*selected, term); ok {
			helperCPU += cpu
			measuredHelperCPU = true
		}
	}
	if measuredHelperCPU {
		t.Logf("helper_lifetime_cpu_total_ns=%d", helperCPU.Nanoseconds())
	}

	t.Logf(
		"backend=%s workload=%s terminals=%d geometry=%dx%d fixture_bytes=%d scroll_lines=%d checksum=%d",
		name, scenario.name, scenario.terminals, scenario.cols, scenario.rows,
		fixtureBytes, scenario.scrollLines, checksum,
	)
}

type terminalMemoryScenario struct {
	name        string
	cols        int
	rows        int
	terminals   int
	scrollLines int
	fixture     []byte
}

func terminalMemoryScenarioForName(t testing.TB, name string) terminalMemoryScenario {
	t.Helper()

	switch name {
	case "reconstruct_4MiB_1term":
		return terminalMemoryScenario{
			name: "reconstruct_4MiB", cols: 120, rows: 40, terminals: 1,
			fixture: syntheticTerminalWorkload(4 * 1024 * 1024),
		}
	case "scroll_12000_1term":
		return terminalMemoryScenario{
			name: "scroll_12000", cols: terminalBenchmarkScrollCols,
			rows: terminalBenchmarkScrollRows, terminals: 1, scrollLines: 12_000,
		}
	case "scroll_24000_1term":
		return terminalMemoryScenario{
			name: "scroll_24000", cols: terminalBenchmarkScrollCols,
			rows: terminalBenchmarkScrollRows, terminals: 1, scrollLines: 24_000,
		}
	case "scroll_12000_3term":
		return terminalMemoryScenario{
			name: "scroll_12000", cols: terminalBenchmarkScrollCols,
			rows: terminalBenchmarkScrollRows, terminals: 3, scrollLines: 12_000,
		}
	case "scroll_24000_3term":
		return terminalMemoryScenario{
			name: "scroll_24000", cols: terminalBenchmarkScrollCols,
			rows: terminalBenchmarkScrollRows, terminals: 3, scrollLines: 24_000,
		}
	default:
		t.Fatalf("unknown terminal memory workload %q", name)

		return terminalMemoryScenario{}
	}
}

func runTerminalMemoryScenario(
	t testing.TB,
	scenario terminalMemoryScenario,
	terms []Terminal,
	checkpoint func(),
) int {
	t.Helper()

	if len(scenario.fixture) > 0 {
		for offset := 0; offset < len(scenario.fixture); offset += terminalWriteChunkBytes {
			end := min(offset+terminalWriteChunkBytes, len(scenario.fixture))
			writeTerminalMemoryChunk(t, terms, scenario.fixture[offset:end])
			checkpoint()
		}

		return len(scenario.fixture)
	}

	fixtureBytes := 0
	for firstLine := 0; firstLine < scenario.scrollLines; firstLine += terminalBenchmarkScrollBatch {
		lineCount := min(terminalBenchmarkScrollBatch, scenario.scrollLines-firstLine)
		chunk := syntheticTerminalScrollLines(firstLine, lineCount)

		fixtureBytes += len(chunk)
		writeTerminalMemoryChunk(t, terms, chunk)
		if firstLine+lineCount == scenario.scrollLines ||
			(firstLine+lineCount)%2048 == 0 {
			checkpoint()
		}
	}

	return fixtureBytes
}

func writeTerminalMemoryChunk(t testing.TB, terms []Terminal, chunk []byte) {
	t.Helper()

	errs := make([]error, len(terms))
	var wait sync.WaitGroup

	for i, term := range terms {
		wait.Add(1)

		go func() {
			defer wait.Done()

			errs[i] = writeTerminalChunks(term, chunk)
		}()
	}

	wait.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func syntheticTerminalScrollLines(firstLine, lineCount int) []byte {
	var fixture strings.Builder
	fixture.Grow(lineCount * 96)

	for lineNumber := firstLine; lineNumber < firstLine+lineCount; lineNumber++ {
		fmt.Fprintf(
			&fixture,
			"\x1b[3%dm%06d canny scrolling line on the wide brae with e\u0301, 你, and 😀\x1b[0m\r\n",
			lineNumber%8,
			lineNumber,
		)
	}

	return []byte(fixture.String())
}

type terminalRSSCheckpoint struct {
	parent      int64
	helperTotal int64
	total       int64
}

type terminalRSSSampler struct {
	pids              []int
	helperCount       int
	available         bool
	samples           int
	latest            terminalRSSCheckpoint
	maxParent         int64
	maxHelper         int64
	maxAggregate      terminalRSSCheckpoint
	maxAggregateIndex int
}

func newTerminalRSSSampler(factory terminalBackendFactory, terms []Terminal) *terminalRSSSampler {
	pids := []int{os.Getpid()}

	for _, term := range terms {
		if pid, ok := terminalHelperPID(factory, term); ok {
			pids = append(pids, pid)
		}
	}

	return &terminalRSSSampler{pids: pids, helperCount: len(pids) - 1, available: true}
}

func (s *terminalRSSSampler) sample(t testing.TB) {
	t.Helper()

	if !s.available {
		return
	}

	rss, ok := terminalBenchmarkCurrentRSS(s.pids)
	if !ok {
		s.available = false

		return
	}
	if len(rss) != len(s.pids) || rss[0] <= 0 {
		t.Fatalf("current RSS samples = %v for %d processes", rss, len(s.pids))
	}

	helperTotal, _, _ := terminalRSSSummary(rss[1:])
	checkpoint := terminalRSSCheckpoint{
		parent: rss[0], helperTotal: helperTotal, total: rss[0] + helperTotal,
	}

	s.samples++
	s.latest = checkpoint
	if checkpoint.parent > s.maxParent {
		s.maxParent = checkpoint.parent
	}
	if checkpoint.helperTotal > s.maxHelper {
		s.maxHelper = checkpoint.helperTotal
	}
	if checkpoint.total > s.maxAggregate.total {
		s.maxAggregate = checkpoint
		s.maxAggregateIndex = s.samples
	}
}

func (s *terminalRSSSampler) report(t testing.TB) {
	t.Helper()

	if !s.available || s.samples == 0 {
		t.Log("sampled_current_rss_unavailable=true")

		return
	}

	t.Logf("rss_checkpoint_samples=%d", s.samples)
	t.Logf("parent_current_rss_bytes=%d", s.latest.parent)
	if s.helperCount > 0 {
		t.Logf(
			"helper_current_rss_count=%d helper_current_rss_total_bytes=%d",
			s.helperCount, s.latest.helperTotal,
		)
	}
	t.Logf("aggregate_current_rss_bytes=%d", s.latest.total)
	t.Logf("parent_sampled_peak_rss_bytes=%d", s.maxParent)
	if s.helperCount > 0 {
		t.Logf("helper_sampled_peak_total_rss_bytes=%d", s.maxHelper)
	}
	t.Logf(
		"aggregate_sampled_peak_rss_bytes=%d aggregate_peak_parent_rss_bytes=%d aggregate_peak_helper_total_rss_bytes=%d aggregate_peak_sample=%d",
		s.maxAggregate.total, s.maxAggregate.parent, s.maxAggregate.helperTotal,
		s.maxAggregateIndex,
	)
}

func reportTerminalPeakRSS(t testing.TB, terms []Terminal) {
	t.Helper()

	_, parentPeakRSS, parentUsageOK := terminalBenchmarkProcessUsage()
	if !parentUsageOK {
		t.Log("lifetime_peak_rss_unavailable=true")

		return
	}
	if parentPeakRSS <= 0 {
		t.Fatal("parent peak RSS is unavailable")
	}

	helperPeaks := make([]int64, 0, len(terms))
	for _, term := range terms {
		if observer, ok := term.(interface{ PeakRSSBytes() int64 }); ok {
			helperPeakRSS := observer.PeakRSSBytes()
			if helperPeakRSS <= 0 {
				t.Fatal("helper peak RSS is unavailable")
			}

			helperPeaks = append(helperPeaks, helperPeakRSS)
		}
	}

	helperTotal, helperMin, helperMax := terminalRSSSummary(helperPeaks)

	t.Logf("parent_lifetime_peak_rss_bytes=%d", parentPeakRSS)
	if len(helperPeaks) > 0 {
		t.Logf(
			"helper_lifetime_peak_count=%d helper_independent_peak_sum_bytes=%d helper_peak_min_bytes=%d helper_peak_max_bytes=%d",
			len(helperPeaks), helperTotal, helperMin, helperMax,
		)
	}
	// Process lifetime peaks are not necessarily simultaneous. Keep this sum
	// explicitly labelled as an estimate and report the sampled aggregate above.
	t.Logf("aggregate_independent_peak_sum_estimate_bytes=%d", parentPeakRSS+helperTotal)
}

func terminalRSSSummary(values []int64) (total, minimum, maximum int64) {
	for i, value := range values {
		total += value

		if i == 0 || value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
	}

	return total, minimum, maximum
}

func TestTerminalSnapshotChecksumCoversRenderedOutput(t *testing.T) {
	base := TerminalSnapshot{
		Cells: []Cell{{Content: "braw", Style: CellStyle{
			FG:   Color{Kind: ColorIndexed, Value: 4},
			BG:   Color{Kind: ColorRGB, Value: 0x0a141e},
			Bold: true,
		}}},
		CursorX:       1,
		CursorVisible: true,
		Cols:          1,
		Rows:          1,
	}

	baseChecksum := terminalSnapshotChecksum(base)
	if got := terminalSnapshotChecksum(base); got != baseChecksum {
		t.Fatalf("checksum is not deterministic: %d != %d", got, baseChecksum)
	}

	variants := []TerminalSnapshot{
		{
			Cells:   []Cell{{Content: "canny", Style: base.Cells[0].Style}},
			CursorX: 1, CursorVisible: true, Cols: 1, Rows: 1,
		},
		{
			Cells: []Cell{{Content: "braw", Style: CellStyle{
				FG: Color{Kind: ColorIndexed, Value: 5}, BG: base.Cells[0].Style.BG, Bold: true,
			}}},
			CursorX: 1, CursorVisible: true, Cols: 1, Rows: 1,
		},
		{
			Cells:   append([]Cell(nil), base.Cells...),
			CursorY: 1, CursorVisible: true, Cols: 1, Rows: 1,
		},
		{
			Cells:   append([]Cell(nil), base.Cells...),
			CursorX: 1, CursorVisible: false, Cols: 1, Rows: 1,
		},
	}

	for i, variant := range variants {
		if got := terminalSnapshotChecksum(variant); got == baseChecksum {
			t.Errorf("variant %d checksum = base checksum %d", i, got)
		}
	}
}

func syntheticTerminalWorkload(minBytes int) []byte {
	var fixture strings.Builder
	fixture.Grow(minBytes + 128)

	for i := 0; fixture.Len() < minBytes; i++ {
		fmt.Fprintf(
			&fixture,
			"\x1b[3%dm%06d canny braw brae e\u0301 你 😀\x1b[0m\r\n",
			i%8,
			i,
		)
	}

	return []byte(fixture.String())
}
