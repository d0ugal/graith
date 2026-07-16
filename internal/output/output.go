package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type Writer struct {
	jsonMode bool
	out      io.Writer
	errOut   io.Writer
}

func New(jsonMode bool) *Writer {
	return &Writer{jsonMode: jsonMode, out: os.Stdout, errOut: os.Stderr}
}

func NewWithWriter(jsonMode bool, w io.Writer) *Writer {
	return &Writer{jsonMode: jsonMode, out: w, errOut: os.Stderr}
}

func (w *Writer) Printf(format string, args ...any) {
	if !w.jsonMode {
		_, _ = fmt.Fprintf(w.out, format, args...)
	}
}

func (w *Writer) JSON(v any) error {
	enc := json.NewEncoder(w.out)
	enc.SetIndent("", "  ")

	return enc.Encode(v)
}

// JSONLine writes v as a single compact JSON object followed by a newline,
// regardless of mode. It is for streaming (NDJSON) output where one object per
// line is emitted incrementally rather than a single document at the end.
func (w *Writer) JSONLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w.out, "%s\n", b)

	return err
}

func (w *Writer) Error(err error) {
	if w.jsonMode {
		type jsonErr struct {
			Error string `json:"error"`
		}

		// Marshal first (a single-string payload can never fail), then write.
		// A write error is unrecoverable and, crucially, we must NOT fall back
		// to a plain-text line in JSON mode — that could append non-JSON after
		// a partial JSON write and break a machine consumer of --json output.
		if b, mErr := json.Marshal(jsonErr{Error: err.Error()}); mErr == nil {
			_, _ = w.errOut.Write(append(b, '\n'))
		}

		return
	}

	_, _ = fmt.Fprintf(w.errOut, "error: %v\n", err)
}

func (w *Writer) IsJSON() bool {
	return w.jsonMode
}
