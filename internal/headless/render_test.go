package headless

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRenderLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "system renders session started",
			line: `{"type":"system","session_id":"braw"}`,
			want: "● session started (braw)\n",
		},
		{
			name: "assistant text renders the text",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"blether"}]}}`,
			want: "blether\n",
		},
		{
			name: "assistant with multiple text blocks concatenates",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"bide "},{"type":"text","text":"a wee"}]}}`,
			want: "bide a wee\n",
		},
		{
			name: "assistant tool_use renders tool name",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}}`,
			want: "● tool: Bash\n",
		},
		{
			name: "assistant with neither text nor tool passes through verbatim",
			line: `{"type":"assistant","message":{"role":"assistant","content":[]}}`,
			want: `{"type":"assistant","message":{"role":"assistant","content":[]}}` + "\n",
		},
		{
			name: "user renders tool result",
			line: `{"type":"user","message":{"role":"user"}}`,
			want: "● tool result\n",
		},
		{
			name: "result renders cost summary",
			line: `{"type":"result","is_error":false,"total_cost_usd":0.1234,"num_turns":3}`,
			want: "● result (turns=3, cost=$0.1234)\n",
		},
		{
			name: "errored result renders error flag",
			line: `{"type":"result","is_error":true,"total_cost_usd":0.5,"num_turns":1}`,
			want: "● result [error] (turns=1, cost=$0.5000)\n",
		},
		{
			name: "result with no cost fields defaults to zero",
			line: `{"type":"result"}`,
			want: "● result (turns=0, cost=$0.0000)\n",
		},
		{
			name: "non-json banner passes through verbatim plus newline",
			line: "dreich crash banner: kaboom",
			want: "dreich crash banner: kaboom\n",
		},
		{
			name: "unknown type passes through verbatim plus newline",
			line: `{"type":"haar","note":"who kens"}`,
			want: `{"type":"haar","note":"who kens"}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := string(renderLine([]byte(tt.line)))
			if got != tt.want {
				t.Fatalf("renderLine(%s)\n got = %q\nwant = %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestReadLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		bufSize int
		max     int
		want    string
		wantErr error
	}{
		{
			name:    "normal line strips newline",
			input:   "braw line\nnext",
			bufSize: 64,
			max:     1024,
			want:    "braw line",
			wantErr: nil,
		},
		{
			name:    "final line without trailing newline at EOF",
			input:   "canny tail",
			bufSize: 64,
			max:     1024,
			want:    "canny tail",
			wantErr: io.EOF,
		},
		{
			name:    "CRLF is stripped",
			input:   "kirk line\r\nrest",
			bufSize: 64,
			max:     1024,
			want:    "kirk line",
			wantErr: nil,
		},
		{
			name:    "line longer than max is truncated",
			input:   strings.Repeat("a", 40) + "\n",
			bufSize: 64,
			max:     10,
			want:    strings.Repeat("a", 10),
			wantErr: nil,
		},
		{
			name:    "line longer than buffer accumulates across ErrBufferFull",
			input:   strings.Repeat("b", 40) + "\n",
			bufSize: 16, // forces bufio.ReadSlice to return ErrBufferFull
			max:     1024,
			want:    strings.Repeat("b", 40),
			wantErr: nil,
		},
		{
			name:    "long line truncated with a small read buffer",
			input:   strings.Repeat("c", 40) + "\n",
			bufSize: 16,
			max:     10,
			want:    strings.Repeat("c", 10),
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := bufio.NewReaderSize(strings.NewReader(tt.input), tt.bufSize)

			got, err := readLine(r, tt.max)
			if string(got) != tt.want {
				t.Fatalf("readLine got = %q, want %q", got, tt.want)
			}

			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("readLine err = %v, want nil", err)
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("readLine err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestReadLineMaxLineBytesNoHang(t *testing.T) {
	t.Parallel()

	// A moderately long line (past the 64KiB default bufio buffer) must be read
	// intact and terminate — it must not panic or hang against the real 16MiB
	// ceiling. This exercises the accumulate-across-ErrBufferFull path with the
	// production max.
	long := strings.Repeat("z", 200*1024)
	r := bufio.NewReaderSize(strings.NewReader(long+"\n"), 64*1024)

	got, err := readLine(r, maxLineBytes)
	if err != nil {
		t.Fatalf("readLine err = %v, want nil", err)
	}

	if string(got) != long {
		t.Fatalf("readLine returned %d bytes, want %d", len(got), len(long))
	}
}

func TestTrimEOL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "bide\n", want: "bide"},
		{in: "bide\r\n", want: "bide"},
		{in: "bide", want: "bide"},
		{in: "bide\r", want: "bide"},
		{in: "", want: ""},
	}
	for _, tt := range tests {
		if got := string(trimEOL([]byte(tt.in))); got != tt.want {
			t.Fatalf("trimEOL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAssistantText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   event
		want string
	}{
		{
			name: "text blocks concatenated",
			ev:   event{Type: "assistant", Message: json.RawMessage(`{"content":[{"type":"text","text":"ken "},{"type":"text","text":"this"}]}`)},
			want: "ken this",
		},
		{
			name: "tool_use blocks ignored",
			ev:   event{Type: "assistant", Message: json.RawMessage(`{"content":[{"type":"tool_use","name":"Bash"}]}`)},
			want: "",
		},
		{
			name: "malformed message returns empty",
			ev:   event{Type: "assistant", Message: json.RawMessage(`{not json`)},
			want: "",
		},
		{
			name: "nil message returns empty",
			ev:   event{Type: "assistant"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := assistantText(tt.ev); got != tt.want {
				t.Fatalf("assistantText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolNameOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   event
		want string
	}{
		{
			name: "first tool_use name",
			ev:   event{Type: "assistant", Message: json.RawMessage(`{"content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"Bash"}]}`)},
			want: "Bash",
		},
		{
			name: "no tool_use returns empty",
			ev:   event{Type: "assistant", Message: json.RawMessage(`{"content":[{"type":"text","text":"hi"}]}`)},
			want: "",
		},
		{
			name: "non-assistant type returns empty",
			ev:   event{Type: "user", Message: json.RawMessage(`{"content":[{"type":"tool_use","name":"Bash"}]}`)},
			want: "",
		},
		{
			name: "malformed message returns empty",
			ev:   event{Type: "assistant", Message: json.RawMessage(`{bad`)},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := toolNameOf(tt.ev); got != tt.want {
				t.Fatalf("toolNameOf = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestControlToolName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "tool_name field", raw: json.RawMessage(`{"tool_name":"Bash"}`), want: "Bash"},
		{name: "name field fallback", raw: json.RawMessage(`{"name":"Edit"}`), want: "Edit"},
		{name: "tool_name preferred over name", raw: json.RawMessage(`{"tool_name":"Bash","name":"Edit"}`), want: "Bash"},
		{name: "empty raw", raw: nil, want: ""},
		{name: "malformed json", raw: json.RawMessage(`{bad`), want: ""},
		{name: "neither field", raw: json.RawMessage(`{"other":"haar"}`), want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := controlToolName(tt.raw); got != tt.want {
				t.Fatalf("controlToolName(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestAppendNL(t *testing.T) {
	t.Parallel()

	got := appendNL([]byte("bonnie"))
	if string(got) != "bonnie\n" {
		t.Fatalf("appendNL = %q, want %q", got, "bonnie\n")
	}

	// The returned slice must be an independent copy — appending to it must not
	// clobber the source backing array.
	src := []byte("loch")
	out := appendNL(src)
	out[0] = 'X'

	if string(src) != "loch" {
		t.Fatalf("appendNL mutated its input: %q", src)
	}
}

func TestIntOr(t *testing.T) {
	t.Parallel()

	if got := intOr(nil); got != 0 {
		t.Fatalf("intOr(nil) = %d, want 0", got)
	}

	n := 7
	if got := intOr(&n); got != 7 {
		t.Fatalf("intOr(&7) = %d, want 7", got)
	}
}
