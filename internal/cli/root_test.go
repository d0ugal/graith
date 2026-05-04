package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestExecuteJSONErrorFormat(t *testing.T) {
	origOut := out
	origJSON := jsonOutput
	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w

	execErr := executeWithArgs([]string{"--json", "nonexistent-command"})

	w.Close()
	os.Stderr = oldStderr

	if execErr == nil {
		t.Fatal("expected error for unknown command")
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

	var jsonErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &jsonErr); err != nil {
		t.Fatalf("stderr is not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if jsonErr.Error == "" {
		t.Error("JSON error message is empty")
	}
}

func TestExecutePlainTextErrorFormat(t *testing.T) {
	origOut := out
	origJSON := jsonOutput
	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w

	execErr := executeWithArgs([]string{"nonexistent-command"})

	w.Close()
	os.Stderr = oldStderr

	if execErr == nil {
		t.Fatal("expected error for unknown command")
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

	got := buf.String()
	if !strings.HasPrefix(got, "error: ") {
		t.Errorf("expected plain text error starting with 'error: ', got %q", got)
	}

	var jsonErr struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(buf.Bytes(), &jsonErr) == nil {
		t.Error("plain text error should not be valid JSON")
	}
}

func TestExecuteCobraSilencesOwnErrors(t *testing.T) {
	origOut := out
	origJSON := jsonOutput
	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	_ = executeWithArgs([]string{"nonexistent-command"})

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)

	if strings.Contains(buf.String(), "Error:") {
		t.Errorf("Cobra's default error message appeared on stdout: %s", buf.String())
	}
}
