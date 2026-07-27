package pty

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func terminalParserPanicFixture(t *testing.T) []byte {
	t.Helper()

	encoded, err := os.ReadFile(filepath.Join("testdata", "scrollup-delete-line-area-panic.hex"))
	if err != nil {
		t.Fatal(err)
	}

	fixture, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}

	return fixture
}
