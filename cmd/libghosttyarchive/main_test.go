package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/libghosttyarchive"
)

func TestRunExitCodes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		args     []string
		wantCode int
		wantErr  string
	}{
		"usage_without_command": {
			wantCode: 2,
			wantErr:  "usage:",
		},
		"usage_for_wrong_pack_arity": {
			args:     []string{"pack", "braw"},
			wantCode: 2,
			wantErr:  "usage:",
		},
		"command_failure": {
			args:     []string{"inspect", filepath.Join(t.TempDir(), "missing.tar.gz")},
			wantCode: 1,
			wantErr:  "no such file",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer

			got := run("libghosttyarchive", test.args, &stderr)
			if got != test.wantCode {
				t.Fatalf("exit code = %d, want %d; stderr=%s", got, test.wantCode, stderr.String())
			}

			if !strings.Contains(stderr.String(), test.wantErr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.wantErr)
			}
		})
	}
}

//nolint:wsl_v5 // The fixture setup and command assertions are intentionally compact.
func TestRunSuccessIsSilent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	source := filepath.Join(root, "bothy-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range libghosttyarchive.AllowedMembers {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	archive := filepath.Join(root, "bothy.tar.gz")

	var stderr bytes.Buffer

	if got := run("libghosttyarchive", []string{"pack", source, archive}, &stderr); got != 0 || stderr.Len() != 0 {
		t.Fatalf("pack exit=%d stderr=%q", got, stderr.String())
	}

	if got := run("libghosttyarchive", []string{"inspect", archive}, &stderr); got != 0 || stderr.Len() != 0 {
		t.Fatalf("inspect exit=%d stderr=%q", got, stderr.String())
	}

	if got := run("libghosttyarchive", []string{"test"}, &stderr); got != 0 || stderr.Len() != 0 {
		t.Fatalf("test exit=%d stderr=%q", got, stderr.String())
	}
}
