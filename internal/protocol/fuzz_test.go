package protocol

import (
	"bytes"
	"testing"
)

func FuzzDecodeControl(f *testing.F) {
	f.Add([]byte(`{"type":"handshake","payload":{"version":"1.0","client_id":"brig","terminal_size":[80,24],"cwd":"/tmp"}}`))
	f.Add([]byte(`{"type":"list"}`))
	f.Add([]byte(`{"type":"create","payload":{"name":"braw","agent":"claude","repo_path":"/croft"}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"type":""}`))
	f.Add([]byte(`{"type":"unknown","payload":null}`))
	f.Add([]byte(`{`))
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"type":123}`))
	f.Add([]byte(`{"type":"x","payload":"not an object"}`))
	f.Add([]byte("\x00\x00\x00"))

	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := DecodeControl(data)
		if err != nil {
			return
		}
		// If decoding succeeded, the envelope should be usable.
		_ = env.Type
		_ = env.Payload

		// Round-trip: re-encode and re-decode should not lose the type.
		reEncoded, err := EncodeControl(env.Type, env.Payload)
		if err != nil {
			return
		}

		env2, err := DecodeControl(reEncoded)
		if err != nil {
			t.Fatalf("round-trip decode failed: %v", err)
		}

		if env2.Type != env.Type {
			t.Fatalf("round-trip type mismatch: %q vs %q", env2.Type, env.Type)
		}
	})
}

func FuzzReadFrame(f *testing.F) {
	// Valid frame: channel=0x00, length=15, payload=`{"type":"list"}`
	validPayload := []byte(`{"type":"list"}`)

	var validFrame bytes.Buffer
	validFrame.WriteByte(ChannelControl)
	validFrame.Write([]byte{0, 0, 0, byte(len(validPayload))}) //nolint:gosec // G115: validPayload is a fixed 15-byte test literal
	validFrame.Write(validPayload)
	f.Add(validFrame.Bytes())

	// Valid data channel frame
	var dataFrame bytes.Buffer
	dataFrame.WriteByte(ChannelData)
	dataFrame.Write([]byte{0, 0, 0, 7})
	dataFrame.Write([]byte("blether"))
	f.Add(dataFrame.Bytes())

	// Empty payload frame
	var emptyFrame bytes.Buffer
	emptyFrame.WriteByte(ChannelControl)
	emptyFrame.Write([]byte{0, 0, 0, 0})
	f.Add(emptyFrame.Bytes())

	// Too-short data
	f.Add([]byte{0x01})
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0})

	// Header claiming oversized payload
	f.Add([]byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF})

	f.Fuzz(func(t *testing.T, data []byte) {
		reader := NewFrameReader(bytes.NewReader(data))

		frame, err := reader.ReadFrame()
		if err != nil {
			return
		}

		// If we got a valid frame, verify round-trip.
		var buf bytes.Buffer

		writer := NewFrameWriter(&buf)
		if err := writer.WriteFrame(frame.Channel, frame.Payload); err != nil {
			return
		}

		reader2 := NewFrameReader(&buf)

		frame2, err := reader2.ReadFrame()
		if err != nil {
			t.Fatalf("round-trip read failed: %v", err)
		}

		if frame2.Channel != frame.Channel {
			t.Fatalf("round-trip channel mismatch: %d vs %d", frame2.Channel, frame.Channel)
		}

		if !bytes.Equal(frame2.Payload, frame.Payload) {
			t.Fatalf("round-trip payload mismatch")
		}
	})
}

func FuzzDecodePayload(f *testing.F) {
	f.Add([]byte(`handshake`), []byte(`{"version":"1.0","client_id":"brig","terminal_size":[80,24],"cwd":"/tmp"}`))
	f.Add([]byte(`create`), []byte(`{"name":"braw","agent":"claude"}`))
	f.Add([]byte(`attach`), []byte(`{"session_id":"abc123"}`))
	f.Add([]byte(`resize`), []byte(`{"cols":120,"rows":40}`))
	f.Add([]byte(`type`), []byte(`{"session_id":"brig","input":"blether","no_newline":true}`))
	f.Add([]byte(`bad`), []byte(`{not json`))
	f.Add([]byte(`empty`), []byte(``))
	f.Add([]byte(`null`), []byte(`null`))

	f.Fuzz(func(t *testing.T, msgType, payloadRaw []byte) {
		env := Envelope{
			Type:    string(msgType),
			Payload: payloadRaw,
		}

		// Try decoding into each known message type — none should panic.
		var h HandshakeMsg

		_ = DecodePayload(env, &h)

		var c CreateMsg

		_ = DecodePayload(env, &c)

		var a AttachMsg

		_ = DecodePayload(env, &a)

		var r ResizeMsg

		_ = DecodePayload(env, &r)

		var l LogsMsg

		_ = DecodePayload(env, &l)

		var tm TypeMsg

		_ = DecodePayload(env, &tm)

		var mp MsgPubMsg

		_ = DecodePayload(env, &mp)

		var ms MsgSubMsg

		_ = DecodePayload(env, &ms)
	})
}
