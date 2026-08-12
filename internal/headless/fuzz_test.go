package headless

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

const maxHeadlessFuzzInputBytes = 4 * 1024

var streamJSONFuzzSeeds = map[string][]byte{
	"system":                   []byte(`{"type":"system","subtype":"init","session_id":"braw"}`),
	"assistant text":           []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"blether"}]}}`),
	"assistant tool use":       []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"printf canny"}}]}}`),
	"user":                     []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"canny"}]}}`),
	"result":                   []byte(`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.1234,"num_turns":3,"duration_ms":42,"duration_api_ms":21,"session_id":"braw","usage":{"input_tokens":7},"result":"done"}`),
	"control request":          []byte(`{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`),
	"control response":         []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"still_queued":[]}}}`),
	"non json banner":          []byte("dreich crash banner: kaboom"),
	"truncated json":           []byte(`{"type":"assistant","message":{"content":[`),
	"array":                    []byte(`[{"type":"assistant"}]`),
	"scalar":                   []byte(`42`),
	"missing message":          []byte(`{"type":"assistant"}`),
	"malformed nested message": []byte(`{"type":"assistant","message":"thrawn"}`),
	"unknown event type":       []byte(`{"type":"haar","note":"who kens"}`),
	"invalid utf8":             {0xff, 0xfe, '{', '"', 't', 'y', 'p', 'e', '"', ':', '"', 'b', 'r', 'a', 'w', '"', '}'},
}

func FuzzRenderLine(f *testing.F) {
	for _, seed := range streamJSONFuzzSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, line []byte) {
		if len(line) > maxHeadlessFuzzInputBytes {
			t.Skip()
		}

		rendered := renderLine(line)

		if len(rendered) == 0 {
			t.Fatal("renderLine returned an empty line")
		}

		if rendered[len(rendered)-1] != '\n' {
			t.Fatalf("renderLine returned a non-newline-terminated line: len=%d", len(rendered))
		}

		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			return
		}

		if ev.Type != "assistant" {
			return
		}

		if text := assistantText(ev); text != "" {
			if want := appendNL([]byte(text)); !bytes.Equal(rendered, want) {
				t.Fatalf("assistant text rendered as %q, want %q", rendered, want)
			}

			return
		}

		if tool := toolNameOf(ev); tool != "" {
			want := []byte("\u25cf tool: " + tool + "\n")
			if !bytes.Equal(rendered, want) {
				t.Fatalf("assistant tool rendered as %q, want %q", rendered, want)
			}
		}
	})
}

func FuzzReadLine(f *testing.F) {
	seeds := map[string]struct {
		input   []byte
		limit   uint16
		bufSize uint8
	}{
		"normal line":         {input: []byte("braw line\nnext"), limit: 1024, bufSize: 64},
		"final line at eof":   {input: []byte("canny tail"), limit: 1024, bufSize: 64},
		"crlf":                {input: []byte("kirk line\r\nrest"), limit: 1024, bufSize: 64},
		"over limit":          {input: append(bytes.Repeat([]byte("a"), 80), '\n', 'b', 'a', 'i', 'r', 'n'), limit: 10, bufSize: 64},
		"over buffer":         {input: append(bytes.Repeat([]byte("b"), 80), '\n'), limit: 256, bufSize: 16},
		"invalid utf8":        {input: []byte{0xff, 0xfe, '\n', 'n', 'e', 'x', 't'}, limit: 32, bufSize: 8},
		"empty":               {input: nil, limit: 32, bufSize: 8},
		"truncated json line": {input: []byte(`{"type":"assistant"`), limit: 32, bufSize: 8},
	}
	for _, seed := range seeds {
		f.Add(seed.input, seed.limit, seed.bufSize)
	}

	f.Fuzz(func(t *testing.T, input []byte, limitSeed uint16, bufSizeSeed uint8) {
		if len(input) > maxHeadlessFuzzInputBytes {
			t.Skip()
		}

		limit := int(limitSeed%512) + 1
		bufSize := int(bufSizeSeed%128) + 1
		r := bufio.NewReaderSize(bytes.NewReader(input), bufSize)

		got, err := readLine(r, limit)
		if len(got) > limit {
			t.Fatalf("readLine returned %d bytes, want at most %d", len(got), limit)
		}

		if bytes.Contains(got, []byte{'\n'}) {
			t.Fatalf("readLine returned an embedded newline: %q", got)
		}

		lineEnd := bytes.IndexByte(input, '\n')

		firstLineLen := len(input)
		if lineEnd >= 0 {
			firstLineLen = lineEnd + 1

			if err != nil {
				t.Fatalf("readLine err = %v, want nil before first newline", err)
			}
		} else if !errors.Is(err, io.EOF) {
			t.Fatalf("readLine err = %v, want EOF for final partial line", err)
		}

		wantPrefix := input[:firstLineLen]
		if len(wantPrefix) > limit {
			wantPrefix = wantPrefix[:limit]
		}

		want := trimEOL(wantPrefix)
		if !bytes.Equal(got, want) {
			t.Fatalf("readLine got %q, want %q", got, want)
		}

		rest, readErr := io.ReadAll(r)
		if readErr != nil {
			t.Fatalf("reading remainder after readLine: %v", readErr)
		}

		if lineEnd >= 0 {
			if wantRest := input[lineEnd+1:]; !bytes.Equal(rest, wantRest) {
				t.Fatalf("readLine left remainder %q, want %q", rest, wantRest)
			}
		} else if len(rest) != 0 {
			t.Fatalf("readLine left %d bytes after EOF", len(rest))
		}
	})
}

func FuzzStatusForEvent(f *testing.F) {
	for _, seed := range streamJSONFuzzSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, line []byte) {
		if len(line) > maxHeadlessFuzzInputBytes {
			t.Skip()
		}

		var ev event

		_ = json.Unmarshal(line, &ev)

		got, ok := statusForEvent(ev)
		switch ev.Type {
		case "system", "assistant", "user":
			if !ok || got != StatusActive {
				t.Fatalf("statusForEvent(%q) = %q, %v; want active, true", ev.Type, got, ok)
			}
		case "result":
			if !ok || got != StatusReady {
				t.Fatalf("statusForEvent(result) = %q, %v; want ready, true", got, ok)
			}
		default:
			if ok || got != "" {
				t.Fatalf("statusForEvent(%q) = %q, %v; want empty, false", ev.Type, got, ok)
			}
		}
	})
}

func FuzzControlSubtypeOf(f *testing.F) {
	seeds := map[string][]byte{
		"empty":        nil,
		"can use tool": []byte(`{"subtype":"can_use_tool","tool_name":"Bash"}`),
		"interrupt":    []byte(`{"subtype":"interrupt"}`),
		"success":      []byte(`{"subtype":"success","request_id":"req-1","response":{}}`),
		"error":        []byte(`{"subtype":"error","request_id":"req-1","error":"dreich"}`),
		"missing":      []byte(`{"tool_name":"Bash"}`),
		"wrong type":   []byte(`{"subtype":42}`),
		"truncated":    []byte(`{"subtype":"interrupt"`),
		"array":        []byte(`[{"subtype":"interrupt"}]`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxHeadlessFuzzInputBytes {
			t.Skip()
		}

		got := controlSubtypeOf(raw)

		var body controlSubtype
		if err := json.Unmarshal(raw, &body); err != nil || len(raw) == 0 {
			if got != "" {
				t.Fatalf("controlSubtypeOf malformed raw returned %q, want empty", got)
			}

			return
		}

		if got != body.Subtype {
			t.Fatalf("controlSubtypeOf returned %q, want %q", got, body.Subtype)
		}
	})
}

func FuzzProtocolShapes(f *testing.F) {
	for _, seed := range streamJSONFuzzSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, line []byte) {
		if len(line) > maxHeadlessFuzzInputBytes {
			t.Skip()
		}

		var ev event

		_ = json.Unmarshal(line, &ev)
		_ = controlSubtypeOf(ev.Request)
		_ = controlSubtypeOf(ev.Response)

		var ctrlReq controlRequest

		_ = json.Unmarshal(line, &ctrlReq)

		var user userMessage

		_ = json.Unmarshal(line, &user)

		var result ResultEnvelope

		_ = json.Unmarshal(line, &result)

		if len(ev.Request) > 0 {
			var canUseTool canUseToolRequest

			_ = json.Unmarshal(ev.Request, &canUseTool)
		}

		if len(ev.Response) > 0 {
			var ctrlResp controlResponse

			_ = json.Unmarshal(ev.Response, &ctrlResp)
		}
	})
}
