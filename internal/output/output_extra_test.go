package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestNewConstructorJSONMode(t *testing.T) {
	tests := []struct {
		name     string
		jsonMode bool
	}{
		{"json mode enabled", true},
		{"json mode disabled", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := New(tt.jsonMode)
			if w.IsJSON() != tt.jsonMode {
				t.Errorf("IsJSON() = %v, want %v", w.IsJSON(), tt.jsonMode)
			}
		})
	}
}

func TestIsJSON(t *testing.T) {
	wJSON := &Writer{jsonMode: true}
	wHuman := &Writer{jsonMode: false}

	if !wJSON.IsJSON() {
		t.Error("expected IsJSON() = true for JSON mode writer")
	}

	if wHuman.IsJSON() {
		t.Error("expected IsJSON() = false for human mode writer")
	}
}

func TestErrorHumanMode(t *testing.T) {
	var errBuf bytes.Buffer

	w := &Writer{jsonMode: false, out: &bytes.Buffer{}, errOut: &errBuf}
	w.Error(errors.New("something went wrong"))

	got := errBuf.String()
	if got != "error: something went wrong\n" {
		t.Errorf("Error() output = %q, want %q", got, "error: something went wrong\n")
	}
}

func TestErrorJSONMode(t *testing.T) {
	var errBuf bytes.Buffer

	w := &Writer{jsonMode: true, out: &bytes.Buffer{}, errOut: &errBuf}
	w.Error(errors.New("json error"))

	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(errBuf.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal JSON error: %v\nbuf: %s", err, errBuf.String())
	}

	if got.Error != "json error" {
		t.Errorf("Error = %q, want %q", got.Error, "json error")
	}
}

func TestErrorDoesNotWriteToStdout(t *testing.T) {
	var outBuf, errBuf bytes.Buffer

	w := &Writer{jsonMode: false, out: &outBuf, errOut: &errBuf}
	w.Error(errors.New("test error"))

	if outBuf.Len() != 0 {
		t.Errorf("Error() should not write to stdout, got %q", outBuf.String())
	}

	if errBuf.Len() == 0 {
		t.Error("Error() should write to stderr")
	}
}

func TestPrintDoesNotWriteToStderr(t *testing.T) {
	var outBuf, errBuf bytes.Buffer

	w := &Writer{jsonMode: false, out: &outBuf, errOut: &errBuf}
	w.Printf("hello %s", "bonnie")

	if errBuf.Len() != 0 {
		t.Errorf("Print() should not write to stderr, got %q", errBuf.String())
	}

	if outBuf.String() != "hello bonnie" {
		t.Errorf("Print() output = %q, want %q", outBuf.String(), "hello bonnie")
	}
}

func TestJSONOutputDoesNotWriteToStderr(t *testing.T) {
	var outBuf, errBuf bytes.Buffer

	w := &Writer{jsonMode: true, out: &outBuf, errOut: &errBuf}

	type data struct {
		Key string `json:"key"`
	}
	if err := w.JSON(data{Key: "neep"}); err != nil {
		t.Fatal(err)
	}

	if errBuf.Len() != 0 {
		t.Errorf("JSON() should not write to stderr, got %q", errBuf.String())
	}

	if outBuf.Len() == 0 {
		t.Error("JSON() should write to stdout")
	}
}
