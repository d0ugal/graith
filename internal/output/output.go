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

func (w *Writer) Print(format string, args ...any) {
	if !w.jsonMode {
		fmt.Fprintf(w.out, format, args...)
	}
}

func (w *Writer) JSON(v any) error {
	enc := json.NewEncoder(w.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (w *Writer) Error(err error) {
	if w.jsonMode {
		type jsonErr struct {
			Error string `json:"error"`
		}
		json.NewEncoder(w.errOut).Encode(jsonErr{Error: err.Error()})
		return
	}
	fmt.Fprintf(w.errOut, "error: %v\n", err)
}

func (w *Writer) IsJSON() bool {
	return w.jsonMode
}
