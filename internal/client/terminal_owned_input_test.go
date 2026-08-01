package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func processTerminalOwnedInput(router *terminalOwnedInputRouter, input []byte) []byte {
	return router.processWithResult(input, false).input
}

func TestTerminalOwnedInputTranslatesMouseThroughChrome(t *testing.T) {
	tests := map[string]struct {
		position string
		input    string
		want     string
	}{
		"top chrome shifts child row": {
			position: "top",
			input:    "\x1b[<0;10;2M",
			want:     "\x1b[<0;10;1M",
		},
		"top chrome drops chrome row": {
			position: "top",
			input:    "\x1b[<0;10;1M",
			want:     "",
		},
		"bottom chrome keeps child row": {
			position: "bottom",
			input:    "\x1b[<0;10;23M",
			want:     "\x1b[<0;10;23M",
		},
		"bottom chrome drops status row": {
			position: "bottom",
			input:    "\x1b[<0;10;24M",
			want:     "",
		},
		"bottom chrome clamps release to child row": {
			position: "bottom",
			input:    "\x1b[<0;10;24m",
			want:     "\x1b[<0;10;23m",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			chrome := newTerminalOwnedAttachChrome(protocol.SessionInfo{Name: "braw"}, false, test.position, 24, 80)
			router := newTerminalOwnedInputRouter(chrome, nil, nil)
			router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
				Cols: 80,
				Rows: 23,
				InputModes: &protocol.TerminalInputModes{
					MouseTracking: protocol.TerminalMouseTrackingNormal,
					MouseFormat:   protocol.TerminalMouseFormatSGR,
				},
			})

			if got := string(processTerminalOwnedInput(router, []byte(test.input))); got != test.want {
				t.Fatalf("processed mouse = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTerminalOwnedInputRoutesWheelByChildMode(t *testing.T) {
	tests := map[string]struct {
		modes     protocol.TerminalInputModes
		want      string
		wantDelta int
		wantCall  bool
	}{
		"mouse tracking receives encoded wheel": {
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNormal,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
			want: "\x1b[<64;5;6M",
		},
		"alternate screen uses alternate scroll cursor key": {
			modes: protocol.TerminalInputModes{
				MouseTracking:         protocol.TerminalMouseTrackingNone,
				MouseFormat:           protocol.TerminalMouseFormatSGR,
				AlternateScreen:       true,
				AlternateScroll:       true,
				ApplicationCursorKeys: true,
			},
			want: applicationCursorUp,
		},
		"primary screen delegates to local history hook": {
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNone,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
			wantDelta: -1,
			wantCall:  true,
		},
		"horizontal wheel is ignored for local history": {
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNone,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var (
				gotDelta int
				gotCall  bool
			)

			router := newTerminalOwnedInputRouter(nil, nil, func(delta int) bool {
				gotDelta = delta
				gotCall = true

				return true
			})
			router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
				Cols:       80,
				Rows:       24,
				InputModes: &test.modes,
			})

			button := 64
			if name == "horizontal wheel is ignored for local history" {
				button = 66
			}

			got := string(processTerminalOwnedInput(router, []byte(fmt.Sprintf("\x1b[<%d;5;6M", button))))
			if got != test.want {
				t.Fatalf("wheel output = %q, want %q", got, test.want)
			}

			if gotCall != test.wantCall {
				t.Fatalf("local history called = %t, want %t", gotCall, test.wantCall)
			}

			if gotDelta != test.wantDelta {
				t.Fatalf("local history delta = %d, want %d", gotDelta, test.wantDelta)
			}
		})
	}
}

func TestTerminalOwnedInputRoutesConfiguredWheelGestures(t *testing.T) {
	tests := map[string]struct {
		policy      string
		bindings    map[string]string
		modes       protocol.TerminalInputModes
		button      int
		want        string
		wantAction  string
		wantHandled bool
	}{
		"off forwards child mouse tracking": {
			policy: config.InputMouseWheelPolicyOff,
			bindings: map[string]string{
				config.InputGestureMouseWheelUp: config.InputActionScrollMode,
			},
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNormal,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
			button: 64,
			want:   "\x1b[<64;5;6M",
		},
		"respect terminal modes captures primary wheel up": {
			policy: config.InputMouseWheelPolicyRespectTerminalModes,
			bindings: map[string]string{
				config.InputGestureMouseWheelUp: config.InputActionScrollMode,
			},
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNone,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
			button:      64,
			wantAction:  config.InputActionScrollMode,
			wantHandled: true,
		},
		"respect terminal modes preserves child mouse tracking": {
			policy: config.InputMouseWheelPolicyRespectTerminalModes,
			bindings: map[string]string{
				config.InputGestureMouseWheelUp: config.InputActionScrollMode,
			},
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNormal,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
			button: 64,
			want:   "\x1b[<64;5;6M",
		},
		"respect terminal modes preserves alternate scroll": {
			policy: config.InputMouseWheelPolicyRespectTerminalModes,
			bindings: map[string]string{
				config.InputGestureMouseWheelUp: config.InputActionScrollMode,
			},
			modes: protocol.TerminalInputModes{
				MouseTracking:         protocol.TerminalMouseTrackingNone,
				MouseFormat:           protocol.TerminalMouseFormatSGR,
				AlternateScreen:       true,
				AlternateScroll:       true,
				ApplicationCursorKeys: true,
			},
			button: 64,
			want:   applicationCursorUp,
		},
		"always captures child mouse tracking": {
			policy: config.InputMouseWheelPolicyAlways,
			bindings: map[string]string{
				config.InputGestureMouseWheelUp: config.InputActionScrollMode,
			},
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNormal,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
			button:      64,
			wantAction:  config.InputActionScrollMode,
			wantHandled: true,
		},
		"always honours explicit none": {
			policy: config.InputMouseWheelPolicyAlways,
			bindings: map[string]string{
				config.InputGestureMouseWheelUp: config.InputActionNone,
			},
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNormal,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
			button: 64,
			want:   "\x1b[<64;5;6M",
		},
		"shift wheel uses shift gesture": {
			policy: config.InputMouseWheelPolicyRespectTerminalModes,
			bindings: map[string]string{
				config.InputGestureShiftMouseWheelUp: config.InputActionScrollMode,
			},
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNone,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
			button:      68,
			wantAction:  config.InputActionScrollMode,
			wantHandled: true,
		},
		"ctrl wheel has no v1 gesture": {
			policy: config.InputMouseWheelPolicyAlways,
			bindings: map[string]string{
				config.InputGestureMouseWheelUp: config.InputActionScrollMode,
			},
			modes: protocol.TerminalInputModes{
				MouseTracking: protocol.TerminalMouseTrackingNormal,
				MouseFormat:   protocol.TerminalMouseFormatSGR,
			},
			button: 80,
			want:   "\x1b[<80;5;6M",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var (
				gotAction  string
				gotHandled bool
			)

			router := newTerminalOwnedInputRouter(nil, nil, nil)
			router.setInputConfig(config.EffectiveInputConfig{
				MouseWheelPolicy: test.policy,
				Bindings:         test.bindings,
			})
			router.setLocalGestureAction(func(action string) bool {
				gotAction = action
				gotHandled = true

				return true
			})
			router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
				Cols:       80,
				Rows:       24,
				InputModes: &test.modes,
			})

			got := string(processTerminalOwnedInput(router, []byte(fmt.Sprintf("\x1b[<%d;5;6M", test.button))))
			if got != test.want {
				t.Fatalf("wheel output = %q, want %q", got, test.want)
			}

			if gotHandled != test.wantHandled {
				t.Fatalf("gesture handled = %t, want %t", gotHandled, test.wantHandled)
			}

			if gotAction != test.wantAction {
				t.Fatalf("gesture action = %q, want %q", gotAction, test.wantAction)
			}
		})
	}
}

func TestTerminalOwnedInputDropsWheelOnChromeRow(t *testing.T) {
	chrome := newTerminalOwnedAttachChrome(protocol.SessionInfo{Name: "braw"}, false, "top", 24, 80)

	var gotDelta int

	router := newTerminalOwnedInputRouter(chrome, nil, func(delta int) bool {
		gotDelta = delta
		return true
	})
	router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
		Cols: 80,
		Rows: 23,
		InputModes: &protocol.TerminalInputModes{
			MouseTracking:   protocol.TerminalMouseTrackingNone,
			MouseFormat:     protocol.TerminalMouseFormatSGR,
			AlternateScreen: true,
			AlternateScroll: true,
		},
	})

	if got := string(processTerminalOwnedInput(router, []byte("\x1b[<64;5;1M"))); got != "" {
		t.Fatalf("wheel on chrome row output = %q, want empty", got)
	}

	if gotDelta != 0 {
		t.Fatalf("wheel on chrome row local history delta = %d, want 0", gotDelta)
	}
}

func TestTerminalOwnedInputDropsUnsupportedSGRPixelsMouse(t *testing.T) {
	router := newTerminalOwnedInputRouter(nil, nil, nil)
	router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
		Cols: 80,
		Rows: 24,
		InputModes: &protocol.TerminalInputModes{
			MouseTracking: protocol.TerminalMouseTrackingNormal,
			MouseFormat:   protocol.TerminalMouseFormatSGRPixels,
		},
	})

	if got := string(processTerminalOwnedInput(router, []byte("\x1b[<0;10;2M"))); got != "" {
		t.Fatalf("SGR-pixels mouse output = %q, want empty", got)
	}
}

func TestTerminalOwnedInputFocusAndPasteModes(t *testing.T) {
	tests := map[string]struct {
		modes protocol.TerminalInputModes
		input string
		want  string
	}{
		"focus enabled forwards focus reports": {
			modes: protocol.TerminalInputModes{Focus: true},
			input: focusInSequence + "braw" + focusOutSequence,
			want:  focusInSequence + "braw" + focusOutSequence,
		},
		"focus disabled drops focus reports": {
			input: focusInSequence + "braw" + focusOutSequence,
			want:  "braw",
		},
		"bracketed paste enabled forwards wrappers": {
			modes: protocol.TerminalInputModes{BracketedPaste: true},
			input: bracketedPasteStart + "dreich\n" + bracketedPasteEnd,
			want:  bracketedPasteStart + "dreich\n" + bracketedPasteEnd,
		},
		"bracketed paste disabled strips stale wrappers": {
			input: bracketedPasteStart + "dreich\n" + bracketedPasteEnd,
			want:  "dreich\n",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			router := newTerminalOwnedInputRouter(nil, nil, nil)
			router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
				Cols:       80,
				Rows:       24,
				InputModes: &test.modes,
			})

			if got := string(processTerminalOwnedInput(router, []byte(test.input))); got != test.want {
				t.Fatalf("processed input = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTerminalOwnedTerminalModeMirror(t *testing.T) {
	var out bytes.Buffer

	mirror := newTerminalOwnedTerminalModeMirror(&out)

	mirror.apply(protocol.TerminalInputModes{
		MouseTracking:         protocol.TerminalMouseTrackingButton,
		MouseFormat:           protocol.TerminalMouseFormatSGR,
		Focus:                 true,
		BracketedPaste:        true,
		ApplicationCursorKeys: true,
		ApplicationKeypad:     true,
	}, false)

	got := out.String()
	for _, want := range []string{
		"\x1b[?1006h",
		"\x1b[?1002h",
		"\x1b[?1004h",
		"\x1b[?2004h",
		"\x1b[?1h",
		"\x1b=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mode enable output %q missing %q", got, want)
		}
	}

	out.Reset()
	mirror.apply(protocol.TerminalInputModes{
		MouseTracking: protocol.TerminalMouseTrackingNone,
		MouseFormat:   protocol.TerminalMouseFormatX10,
	}, false)

	got = out.String()
	for _, want := range []string{
		"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l",
		"\x1b[?1004l",
		"\x1b[?2004l",
		"\x1b[?1l",
		"\x1b>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mode disable output %q missing %q", got, want)
		}
	}

	out.Reset()
	mirror.apply(protocol.TerminalInputModes{
		MouseTracking: protocol.TerminalMouseTrackingNormal,
		MouseFormat:   protocol.TerminalMouseFormatSGRPixels,
	}, false)

	got = out.String()
	if strings.Contains(got, "\x1b[?1000h") || strings.Contains(got, "\x1b[?1006h") {
		t.Fatalf("SGR-pixels mirror output enabled cell mouse reporting: %q", got)
	}
}

func TestTerminalOwnedReadOnlyDoesNotMirrorTerminalModes(t *testing.T) {
	var out bytes.Buffer

	router := newTerminalOwnedInputRouter(nil, newTerminalOwnedTerminalModeMirror(&out), nil)
	router.readOnly = true
	router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
		Cols: 80,
		Rows: 24,
		InputModes: &protocol.TerminalInputModes{
			MouseTracking:  protocol.TerminalMouseTrackingNormal,
			MouseFormat:    protocol.TerminalMouseFormatSGR,
			Focus:          true,
			BracketedPaste: true,
		},
	})

	if out.Len() != 0 {
		t.Fatalf("read-only mirror output = %q, want empty", out.String())
	}
}

func TestTerminalOwnedReadOnlyDropsRoutedMouseInput(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	received := captureDaemonDataFrames(daemonConn, 1)

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}
	router := newTerminalOwnedInputRouter(nil, nil, nil)
	router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
		Cols: 80,
		Rows: 24,
		InputModes: &protocol.TerminalInputModes{
			MouseTracking: protocol.TerminalMouseTrackingNormal,
			MouseFormat:   protocol.TerminalMouseFormatSGR,
		},
	})

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte("\x1b[<0;5;5M"))

		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'd'})
	}()

	opts := testOpts
	opts.ReadOnly = true
	opts.terminalOwnedInput = router

	result := c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil)
	if result != ResultDetached {
		t.Fatalf("result = %d, want detached", result)
	}

	select {
	case data := <-received:
		t.Fatalf("read-only terminal-owned attach forwarded mouse input: %q", data)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestTerminalOwnedWheelGestureReturnsMouseScrollMode(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}
	router := newTerminalOwnedInputRouter(nil, nil, nil)
	router.setInputConfig(config.EffectiveInputConfig{
		MouseWheelPolicy: config.InputMouseWheelPolicyRespectTerminalModes,
		Bindings: map[string]string{
			config.InputGestureMouseWheelUp: config.InputActionScrollMode,
		},
	})
	router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
		Cols: 80,
		Rows: 24,
		InputModes: &protocol.TerminalInputModes{
			MouseTracking: protocol.TerminalMouseTrackingNone,
			MouseFormat:   protocol.TerminalMouseFormatSGR,
		},
	})

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte("\x1b[<64;5;5M"))
	}()

	opts := testOpts
	opts.terminalOwnedInput = router

	if result := c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil); result != ResultMouseScrollMode {
		t.Fatalf("result = %d, want ResultMouseScrollMode (%d)", result, ResultMouseScrollMode)
	}
}

func TestTerminalOwnedWheelGestureForwardsEarlierBytesBeforeScrollMode(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	received := captureDaemonDataFrames(daemonConn, 1)
	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}
	router := newTerminalOwnedInputRouter(nil, nil, nil)
	router.setInputConfig(config.EffectiveInputConfig{
		MouseWheelPolicy: config.InputMouseWheelPolicyRespectTerminalModes,
		Bindings: map[string]string{
			config.InputGestureMouseWheelUp: config.InputActionScrollMode,
		},
	})
	router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
		Cols: 80,
		Rows: 24,
		InputModes: &protocol.TerminalInputModes{
			MouseTracking: protocol.TerminalMouseTrackingNone,
			MouseFormat:   protocol.TerminalMouseFormatSGR,
		},
	})

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte("braw\x1b[<64;5;5M"))
	}()

	opts := testOpts
	opts.terminalOwnedInput = router

	result := c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil)
	if result != ResultMouseScrollMode {
		t.Fatalf("result = %d, want ResultMouseScrollMode (%d)", result, ResultMouseScrollMode)
	}

	select {
	case data := <-received:
		if string(data) != "braw" {
			t.Fatalf("forwarded bytes = %q, want braw", data)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for forwarded bytes before wheel action")
	}
}

func TestTerminalOwnedWheelGestureUsesTopChromeTranslatedChildRow(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}
	chrome := newTerminalOwnedAttachChrome(protocol.SessionInfo{Name: "braw"}, false, "top", 24, 80)
	router := newTerminalOwnedInputRouter(chrome, nil, nil)
	router.setInputConfig(config.EffectiveInputConfig{
		MouseWheelPolicy: config.InputMouseWheelPolicyRespectTerminalModes,
		Bindings: map[string]string{
			config.InputGestureMouseWheelUp: config.InputActionScrollMode,
		},
	})
	router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
		Cols: 80,
		Rows: 23,
		InputModes: &protocol.TerminalInputModes{
			MouseTracking: protocol.TerminalMouseTrackingNone,
			MouseFormat:   protocol.TerminalMouseFormatSGR,
		},
	})

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte("\x1b[<64;5;2M"))
	}()

	opts := testOpts
	opts.TerminalOwned = true
	opts.terminalOwnedChrome = chrome
	opts.terminalOwnedInput = router

	if result := c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil); result != ResultMouseScrollMode {
		t.Fatalf("result = %d, want ResultMouseScrollMode (%d)", result, ResultMouseScrollMode)
	}
}

func TestTerminalOwnedKeyboardLockedDropsChildInputButKeepsPrefixDetach(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	received := captureDaemonDataFrames(daemonConn, 1)
	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}
	router := newTerminalOwnedInputRouter(nil, nil, nil)
	router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
		Cols: 80,
		Rows: 24,
		InputModes: &protocol.TerminalInputModes{
			KeyboardLocked: true,
		},
	})

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte("braw"))

		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'd'})
	}()

	opts := testOpts
	opts.terminalOwnedInput = router

	result := c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil)
	if result != ResultDetached {
		t.Fatalf("result = %d, want detached", result)
	}

	select {
	case data := <-received:
		t.Fatalf("keyboard-locked terminal-owned attach forwarded child input: %q", data)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestTerminalOwnedBracketedPasteBypassesLocalPrefix(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)
	received := captureDaemonDataFrames(daemonConn, 1)
	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}
	router := newTerminalOwnedInputRouter(nil, nil, nil)
	router.updateSnapshot(&protocol.ScreenSnapshotResponseMsg{
		Cols: 80,
		Rows: 24,
		InputModes: &protocol.TerminalInputModes{
			BracketedPaste: true,
		},
	})

	pasted := bracketedPasteStart + "braw" + string([]byte{0x02, 'd'}) + "dreich" + bracketedPasteEnd

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte(pasted + string([]byte{0x02, 'd'})))
	}()

	opts := testOpts
	opts.terminalOwnedInput = router

	result := c.runPassthroughLoop(context.Background(), opts, stdinR, stdout, nil)
	if result != ResultDetached {
		t.Fatalf("result = %d, want detached", result)
	}

	select {
	case data := <-received:
		if string(data) != pasted {
			t.Fatalf("forwarded paste = %q, want %q", data, pasted)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for forwarded paste")
	}
}

func captureDaemonDataFrames(conn net.Conn, capacity int) chan []byte {
	received := make(chan []byte, capacity)
	daemonReader := protocol.NewFrameReader(conn)

	go func() {
		for {
			frame, err := daemonReader.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelData {
				received <- append([]byte{}, frame.Payload...)
			}
		}
	}()

	return received
}
