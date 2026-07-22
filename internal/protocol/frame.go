package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	ChannelControl = byte(0x00)
	ChannelData    = byte(0x01)
	MaxPayload     = 4 * 1024 * 1024
	// MaxStoreDocumentBytes leaves room for the JSON control envelope and
	// escaping below MaxPayload. Store reads and scenario result publication use
	// the same limit so every accepted result remains consumable via store_get.
	MaxStoreDocumentBytes      = MaxPayload - 64*1024
	MaxScenarioResultBodyBytes = MaxStoreDocumentBytes
	// MaxScenarioPromptBytes treats scenario startup instructions as a body,
	// rather than coupling them to the much smaller todo-title limit. Keeping the
	// per-member cap below common per-argument process limits and the frame ceiling
	// leaves room for both agent launch and the rest of a multi-member start.
	MaxScenarioPromptBytes = 64 * 1024
	// MaxScenarioContractPayloadBytes bounds the JSON-encoded prompt/task fields
	// for one scenario roster. The remaining 1 MiB of a control frame is reserved
	// for member metadata, the envelope, and status fields.
	MaxScenarioContractPayloadBytes = MaxPayload - 1024*1024
	headerSize                      = 5
)

type Frame struct {
	Channel byte
	Payload []byte
}

type FrameWriter struct {
	w io.Writer
}

func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: w}
}

func (fw *FrameWriter) WriteFrame(channel byte, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("payload too large: %d bytes (max %d)", len(payload), MaxPayload)
	}

	buf := make([]byte, headerSize+len(payload))
	buf[0] = channel
	binary.BigEndian.PutUint32(buf[1:headerSize], uint32(len(payload))) //nolint:gosec // G115: len(payload) is bounded by MaxPayload (4MiB) above
	copy(buf[headerSize:], payload)

	if _, err := fw.w.Write(buf); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}

	return nil
}

type FrameReader struct {
	r   io.Reader
	hdr [headerSize]byte
}

func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{r: r}
}

func (fr *FrameReader) ReadFrame() (Frame, error) {
	if _, err := io.ReadFull(fr.r, fr.hdr[:]); err != nil {
		return Frame{}, err
	}

	channel := fr.hdr[0]

	length := binary.BigEndian.Uint32(fr.hdr[1:])
	if length > MaxPayload {
		return Frame{}, fmt.Errorf("frame too large: %d bytes (max %d)", length, MaxPayload)
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(fr.r, payload); err != nil {
			return Frame{}, fmt.Errorf("read frame payload: %w", err)
		}
	}

	return Frame{Channel: channel, Payload: payload}, nil
}
