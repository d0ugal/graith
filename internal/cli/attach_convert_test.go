package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

// TestAttachWithConvertPlainAttach: a non-headless session attaches on the first
// round-trip and returns attached=true with the decoded info.
func TestAttachWithConvertPlainAttach(t *testing.T) {
	withDiscardOutput(t)

	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("attached", protocol.SessionInfo{ID: "braw", Name: "bonnie"})),
	}}

	info, attached, err := attachWithConvert(c, "braw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !attached {
		t.Fatal("expected attached=true")
	}

	if info.ID != "braw" || info.Name != "bonnie" {
		t.Errorf("info = %+v, want {braw bonnie}", info)
	}

	if got := c.sentTypes(); len(got) != 1 || got[0] != "attach" {
		t.Errorf("sent = %v, want one attach", got)
	}
}

// TestAttachWithConvertReadError: a transport failure on the first read is
// returned verbatim.
func TestAttachWithConvertReadError(t *testing.T) {
	withDiscardOutput(t)

	sentinel := errors.New("socket gone")
	c := &scriptedConn{responses: []scriptedResp{errResp(sentinel)}}

	_, attached, err := attachWithConvert(c, "braw")
	if attached || !errors.Is(err, sentinel) {
		t.Fatalf("attached=%v err=%v, want attached=false err=%v", attached, err, sentinel)
	}
}

// TestAttachWithConvertDaemonError: an "error" envelope aborts with the message.
func TestAttachWithConvertDaemonError(t *testing.T) {
	withDiscardOutput(t)

	c := &scriptedConn{responses: []scriptedResp{okResp(errEnv("session fashed"))}}

	_, attached, err := attachWithConvert(c, "braw")
	if attached || err == nil || !strings.Contains(err.Error(), "session fashed") {
		t.Fatalf("attached=%v err=%v, want error containing \"session fashed\"", attached, err)
	}
}

// TestAttachWithConvertDeclined: convert_required + a declined prompt returns
// attached=false, err=nil, and never sends attach_convert.
func TestAttachWithConvertDeclined(t *testing.T) {
	withDiscardOutput(t)

	orig := attachYes
	attachYes = false

	defer func() { attachYes = orig }()

	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("convert_required", protocol.ConvertRequiredMsg{Name: "haar"})),
	}}

	var (
		attached bool
		err      error
	)

	withStdinPipe(t, "n\n", func() {
		_, attached, err = attachWithConvert(c, "haar")
	})

	if attached || err != nil {
		t.Fatalf("attached=%v err=%v, want attached=false err=nil", attached, err)
	}

	if got := c.sentTypes(); len(got) != 1 || got[0] != "attach" {
		t.Errorf("sent = %v, want only the initial attach (no attach_convert)", got)
	}
}

// TestAttachWithConvertConfirmed: convert_required → confirm → converted →
// attach again → attached. The happy path through the whole handshake.
func TestAttachWithConvertConfirmed(t *testing.T) {
	withDiscardOutput(t)

	orig := attachYes
	attachYes = true // skip the prompt

	defer func() { attachYes = orig }()

	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("convert_required", protocol.ConvertRequiredMsg{Name: "haar"})),
		okResp(typeEnv("converted")),
		okResp(payloadEnv("attached", protocol.SessionInfo{ID: "haar", Name: "cleared"})),
	}}

	info, attached, err := attachWithConvert(c, "haar")
	if err != nil || !attached {
		t.Fatalf("attached=%v err=%v, want attached=true err=nil", attached, err)
	}

	if info.Name != "cleared" {
		t.Errorf("info.Name = %q, want cleared", info.Name)
	}

	want := []string{"attach", "attach_convert", "attach"}
	if got := c.sentTypes(); !equalStrings(got, want) {
		t.Errorf("sent = %v, want %v", got, want)
	}
}

// TestAttachWithConvertStillHeadless: if the daemon replies convert_required a
// second time (after a convert), the loop bails instead of spinning forever.
func TestAttachWithConvertStillHeadless(t *testing.T) {
	withDiscardOutput(t)

	orig := attachYes
	attachYes = true

	defer func() { attachYes = orig }()

	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("convert_required", protocol.ConvertRequiredMsg{Name: "haar"})),
		okResp(typeEnv("converted")),
		okResp(payloadEnv("convert_required", protocol.ConvertRequiredMsg{Name: "haar"})),
	}}

	_, attached, err := attachWithConvert(c, "haar")
	if attached || err == nil || !strings.Contains(err.Error(), "still headless after convert") {
		t.Fatalf("attached=%v err=%v, want \"still headless after convert\"", attached, err)
	}
}

// TestAttachWithConvertConvertError: attach_convert answered with an error is
// reported as a convert failure.
func TestAttachWithConvertConvertError(t *testing.T) {
	withDiscardOutput(t)

	orig := attachYes
	attachYes = true

	defer func() { attachYes = orig }()

	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("convert_required", protocol.ConvertRequiredMsg{Name: "haar"})),
		okResp(errEnv("relaunch failed")),
	}}

	_, attached, err := attachWithConvert(c, "haar")
	if attached || err == nil || !strings.Contains(err.Error(), "convert failed: relaunch failed") {
		t.Fatalf("attached=%v err=%v, want \"convert failed: relaunch failed\"", attached, err)
	}
}

// TestAttachWithConvertUnexpectedConvertReply: a non-error, non-"converted"
// reply to attach_convert must not advance the handshake.
func TestAttachWithConvertUnexpectedConvertReply(t *testing.T) {
	withDiscardOutput(t)

	orig := attachYes
	attachYes = true

	defer func() { attachYes = orig }()

	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("convert_required", protocol.ConvertRequiredMsg{Name: "haar"})),
		okResp(typeEnv("whit")),
	}}

	_, attached, err := attachWithConvert(c, "haar")
	if attached || err == nil || !strings.Contains(err.Error(), "unexpected response to attach_convert") {
		t.Fatalf("attached=%v err=%v, want unexpected-attach_convert error", attached, err)
	}
}

// TestAttachWithConvertConvertReadError: a transport failure while reading the
// attach_convert reply is returned.
func TestAttachWithConvertConvertReadError(t *testing.T) {
	withDiscardOutput(t)

	orig := attachYes
	attachYes = true

	defer func() { attachYes = orig }()

	sentinel := errors.New("pipe broke")
	c := &scriptedConn{responses: []scriptedResp{
		okResp(payloadEnv("convert_required", protocol.ConvertRequiredMsg{Name: "haar"})),
		errResp(sentinel),
	}}

	_, attached, err := attachWithConvert(c, "haar")
	if attached || !errors.Is(err, sentinel) {
		t.Fatalf("attached=%v err=%v, want %v", attached, err, sentinel)
	}
}

// TestAttachWithConvertUnexpectedType: an unrecognised first reply is rejected.
func TestAttachWithConvertUnexpectedType(t *testing.T) {
	withDiscardOutput(t)

	c := &scriptedConn{responses: []scriptedResp{okResp(typeEnv("blether"))}}

	_, attached, err := attachWithConvert(c, "braw")
	if attached || err == nil || !strings.Contains(err.Error(), "unexpected response to attach") {
		t.Fatalf("attached=%v err=%v, want unexpected-attach error", attached, err)
	}
}

// TestAttachWithConvertAttachedDecodeError: an "attached" reply with no payload
// surfaces a decode error rather than a bogus success.
func TestAttachWithConvertAttachedDecodeError(t *testing.T) {
	withDiscardOutput(t)

	c := &scriptedConn{responses: []scriptedResp{okResp(typeEnv("attached"))}}

	_, attached, err := attachWithConvert(c, "braw")
	if attached || err == nil || !strings.Contains(err.Error(), "decode attach response") {
		t.Fatalf("attached=%v err=%v, want decode error", attached, err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
