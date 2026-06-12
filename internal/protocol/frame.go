package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	ChannelControl = byte(0x00)
	ChannelData    = byte(0x01)
	ChannelMCP     = byte(0x02)
	MaxPayload     = 4 * 1024 * 1024
	headerSize     = 5
)

type Frame struct {
	Channel byte
	Payload []byte
}

type FrameWriter struct {
	w   io.Writer
	hdr [headerSize]byte
}

func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: w}
}

func (fw *FrameWriter) WriteFrame(channel byte, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("payload too large: %d bytes (max %d)", len(payload), MaxPayload)
	}
	fw.hdr[0] = channel
	binary.BigEndian.PutUint32(fw.hdr[1:], uint32(len(payload)))
	if _, err := fw.w.Write(fw.hdr[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if len(payload) > 0 {
		if _, err := fw.w.Write(payload); err != nil {
			return fmt.Errorf("write frame payload: %w", err)
		}
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
