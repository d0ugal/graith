package headless

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	grpty "github.com/d0ugal/graith/internal/pty"
)

// screenPreviewBytes bounds how much scrollback tail the preview/snapshot
// renders — enough for a useful glimpse, not the whole log.
const screenPreviewBytes = 16 * 1024

// ScreenPreview returns a plain-text tail of the rendered scrollback. A
// headless session has no vt10x screen, so the overlay preview and the
// screen_preview control message degrade to the recent rendered output.
func (s *Session) ScreenPreview() string {
	tail, err := s.scrollback.TailBytes(screenPreviewBytes)
	if err != nil {
		return ""
	}

	return string(tail)
}

// ScreenSnapshot returns a ScreenCapture whose Frame is the scrollback tail, so
// callers expecting a PTY snapshot get something sensible for a headless
// session.
func (s *Session) ScreenSnapshot() grpty.ScreenCapture {
	return grpty.ScreenCapture{Frame: s.ScreenPreview()}
}

// readLine reads one newline-terminated line from r, accumulating across the
// buffer boundary (ReadSlice returns ErrBufferFull for lines longer than the
// buffer). Lines longer than limit are truncated to limit bytes and the
// remainder discarded, so a pathological line can't exhaust memory. The trailing
// CR/LF is stripped. The returned error (e.g. io.EOF) accompanies any final
// partial line.
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte

	for {
		frag, err := r.ReadSlice('\n')

		if len(buf) < limit {
			room := limit - len(buf)
			if room >= len(frag) {
				buf = append(buf, frag...)
			} else {
				buf = append(buf, frag[:room]...)
			}
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue // line longer than the read buffer; keep accumulating
		}

		return trimEOL(buf), err
	}
}

func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\r"))

	return b
}

// renderLine turns a stream-json line into a human-readable scrollback line
// (always newline-terminated). Recognised events render compactly; anything
// unrecognised (including non-JSON banners) passes through verbatim so nothing
// is silently lost.
func renderLine(line []byte) []byte {
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		return appendNL(line)
	}

	switch ev.Type {
	case "system":
		return []byte(fmt.Sprintf("● session started (%s)\n", ev.SessionID))
	case "assistant":
		if txt := assistantText(ev); txt != "" {
			return appendNL([]byte(txt))
		}

		if tool := toolNameOf(ev); tool != "" {
			return []byte(fmt.Sprintf("● tool: %s\n", tool))
		}

		return appendNL(line)
	case "user":
		return []byte("● tool result\n")
	case "result":
		errFlag := ""
		if ev.IsError != nil && *ev.IsError {
			errFlag = " [error]"
		}

		cost := 0.0
		if ev.TotalCost != nil {
			cost = *ev.TotalCost
		}

		return []byte(fmt.Sprintf("● result%s (turns=%d, cost=$%.4f)\n", errFlag, intOr(ev.NumTurns), cost))
	default:
		return appendNL(line)
	}
}

func appendNL(b []byte) []byte {
	out := make([]byte, len(b), len(b)+1)
	copy(out, b)

	return append(out, '\n')
}

// assistantText concatenates the text blocks of an assistant message.
func assistantText(ev event) string {
	var msg assistantMessage
	if err := json.Unmarshal(ev.Message, &msg); err != nil {
		return ""
	}

	var out bytes.Buffer

	for _, c := range msg.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
		}
	}

	return out.String()
}

// toolNameOf returns the name of the first tool_use block in an assistant
// message, or "" if none.
func toolNameOf(ev event) string {
	if ev.Type != "assistant" {
		return ""
	}

	var msg assistantMessage
	if err := json.Unmarshal(ev.Message, &msg); err != nil {
		return ""
	}

	for _, c := range msg.Content {
		if c.Type == "tool_use" {
			return c.Name
		}
	}

	return ""
}

// controlToolName best-effort extracts a tool name from a can_use_tool control
// request body (schema is SDK-internal; tolerate absence).
func controlToolName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var body struct {
		ToolName string `json:"tool_name"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}

	if body.ToolName != "" {
		return body.ToolName
	}

	return body.Name
}

func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}
