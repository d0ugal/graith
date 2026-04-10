package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWriteFrameExceedsMaxPayload(t *testing.T) {
	var buf bytes.Buffer
	fw := NewFrameWriter(&buf)
	payload := make([]byte, MaxPayload+1)
	err := fw.WriteFrame(ChannelData, payload)
	if err == nil {
		t.Fatal("expected error for payload exceeding MaxPayload")
	}
}

func TestReadFrameCorruptedLengthHeader(t *testing.T) {
	// Craft a header that claims a length greater than MaxPayload.
	var buf bytes.Buffer
	buf.WriteByte(ChannelData)
	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, MaxPayload+1)
	buf.Write(lengthBytes)

	fr := NewFrameReader(&buf)
	_, err := fr.ReadFrame()
	if err == nil {
		t.Fatal("expected error for corrupted length header exceeding MaxPayload")
	}
}

func TestEncodeControlUnmarshalableValue(t *testing.T) {
	ch := make(chan int)
	_, err := EncodeControl("bad", ch)
	if err == nil {
		t.Fatal("expected error when encoding a channel value")
	}
}

func TestDecodeControlInvalidJSON(t *testing.T) {
	_, err := DecodeControl([]byte(`{not valid json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestDecodePayloadMismatchedType(t *testing.T) {
	// Encode a HandshakeMsg, then try to decode into a CreateMsg.
	data, err := EncodeControl("handshake", HandshakeMsg{
		Version:      "1.0",
		ClientID:     "test",
		TerminalSize: [2]uint16{80, 24},
		Cwd:          "/tmp",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}

	var create CreateMsg
	if err := DecodePayload(env, &create); err != nil {
		t.Fatal(err)
	}
	// The mismatched fields should be zero-valued because JSON unmarshal
	// silently ignores unknown keys and leaves missing keys at zero value.
	if create.Name != "" {
		t.Errorf("expected empty Name, got %q", create.Name)
	}
	if create.Agent != "" {
		t.Errorf("expected empty Agent, got %q", create.Agent)
	}
	if create.RepoPath != "" {
		t.Errorf("expected empty RepoPath, got %q", create.RepoPath)
	}
}

func TestDecodePayloadInvalidJSON(t *testing.T) {
	env := Envelope{Type: "bad", Payload: []byte(`{not json`)}
	var target HandshakeMsg
	err := DecodePayload(env, &target)
	if err == nil {
		t.Fatal("expected error decoding invalid JSON payload")
	}
}

func TestReadFrameShortHeader(t *testing.T) {
	// Provide fewer than headerSize bytes to trigger an io read error.
	buf := bytes.NewReader([]byte{0x01, 0x00})
	fr := NewFrameReader(buf)
	_, err := fr.ReadFrame()
	if err == nil {
		t.Fatal("expected error for short header")
	}
}

func TestReadFrameShortPayload(t *testing.T) {
	// Header claims 10 bytes of payload but only 3 are present.
	var buf bytes.Buffer
	buf.WriteByte(ChannelData)
	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, 10)
	buf.Write(lengthBytes)
	buf.Write([]byte("abc")) // only 3 of the claimed 10

	fr := NewFrameReader(&buf)
	_, err := fr.ReadFrame()
	if err == nil {
		t.Fatal("expected error for truncated payload")
	}
}

func TestWriteFrameExactMaxPayload(t *testing.T) {
	var buf bytes.Buffer
	fw := NewFrameWriter(&buf)
	payload := make([]byte, MaxPayload) // exactly at the limit
	err := fw.WriteFrame(ChannelData, payload)
	if err != nil {
		t.Fatalf("expected no error for payload at exactly MaxPayload, got: %v", err)
	}
}
