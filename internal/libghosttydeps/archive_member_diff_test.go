package libghosttydeps

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportArchiveMemberDiffNamesChangedMembers(t *testing.T) {
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	script, err := os.ReadFile(filepath.Join(sourceRoot, "scripts", "libghostty-native.sh"))
	if err != nil {
		t.Fatal(err)
	}

	const start = "report_archive_member_diff() {"

	text := string(script)

	begin := strings.Index(text, start)
	if begin < 0 {
		t.Fatalf("scripts/libghostty-native.sh has no %s", start)
	}

	end := strings.Index(text[begin:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of report_archive_member_diff")
	}

	helper := text[begin : begin+end+3]

	tests := map[string]struct {
		expected    string
		actual      string
		wantMissing []string
		wantExtra   []string
	}{
		"added and removed members": {
			expected:    "braw.o\ncanny.o\ndreich.o",
			actual:      "braw.o\ndreich.o\nthrawn.o",
			wantMissing: []string{"  - canny.o"},
			wantExtra:   []string{"  + thrawn.o"},
		},
		"only an extra member": {
			expected:  "braw.o\ncanny.o",
			actual:    "braw.o\ncanny.o\nbothy.o",
			wantExtra: []string{"  + bothy.o"},
		},
		"unsorted input is compared as sets": {
			expected:    "dreich.o\nbraw.o",
			actual:      "braw.o\ncroft.o",
			wantMissing: []string{"  - dreich.o"},
			wantExtra:   []string{"  + croft.o"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+helper+"\nreport_archive_member_diff \"$EXPECTED\" \"$ACTUAL\"")

			cmd.Env = append(os.Environ(), "EXPECTED="+test.expected, "ACTUAL="+test.actual)

			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper must never fail: %v\n%s", err, output)
			}

			got := string(output)

			for _, want := range append(test.wantMissing, test.wantExtra...) {
				if !strings.Contains(got, want+"\n") {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}

			if len(test.wantMissing) == 0 && !strings.Contains(got, "missing from the archive:\n  - (none)\n") {
				t.Errorf("expected (none) for missing members:\n%s", got)
			}
		})
	}
}
