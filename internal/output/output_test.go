package output

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestJSONOutput(t *testing.T) {
	var buf bytes.Buffer

	w := &Writer{jsonMode: true, out: &buf, errOut: &buf}

	type data struct {
		Name string `json:"name"`
	}
	w.JSON(data{Name: "braw"})

	var got data
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbuf: %s", err, buf.String())
	}

	if got.Name != "braw" {
		t.Errorf("Name = %q, want braw", got.Name)
	}
}

func TestHumanOutput(t *testing.T) {
	var buf bytes.Buffer

	w := &Writer{jsonMode: false, out: &buf, errOut: &buf}
	w.Printf("hello %s\n", "bonnie")

	if buf.String() != "hello bonnie\n" {
		t.Errorf("output = %q", buf.String())
	}
}

func TestPrintSuppressedInJSONMode(t *testing.T) {
	var buf bytes.Buffer

	w := &Writer{jsonMode: true, out: &buf, errOut: &buf}
	w.Printf("should not appear")

	if buf.Len() != 0 {
		t.Errorf("Print should be suppressed in JSON mode, got %q", buf.String())
	}
}
