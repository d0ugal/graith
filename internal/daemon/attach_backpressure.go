package daemon

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

const (
	defaultAttachOutputQueueMaxBytes  = 1 << 20
	defaultAttachOutputQueueMaxChunks = 16 * 1024
	defaultAttachOutputCoalesceBytes  = 32 * 1024
	defaultAttachOutputWriteTimeout   = 2 * time.Second
)

type attachOutputMode string

const (
	attachOutputRaw       attachOutputMode = "raw"
	attachOutputCoalesced attachOutputMode = "coalesced"
)

type attachOutputWriter interface {
	io.Writer
	Close()
}

type attachDataWriterConfig struct {
	SessionID     string
	Writer        *safeFrameWriter
	Conn          net.Conn
	Log           *slog.Logger
	Mode          attachOutputMode
	MaxBytes      int
	MaxChunks     int
	CoalesceBytes int
	WriteTimeout  time.Duration

	writeFrame func([]byte) error
	closeConn  func() error
}

type attachOutputStats struct {
	enqueuedFrames int64
	enqueuedBytes  int64
	writtenFrames  int64
	writtenBytes   int64
	coalesced      int64
	droppedFrames  int64
	droppedBytes   int64
}

type boundedAttachDataWriter struct {
	mu   sync.Mutex
	cond *sync.Cond
	done chan struct{}

	sessionID string
	log       *slog.Logger
	mode      attachOutputMode

	maxBytes      int
	maxChunks     int
	coalesceBytes int
	writeFrame    func([]byte) error
	closeConn     func() error

	queue       [][]byte
	queuedBytes int
	closed      bool
	overflowed  bool
	writeErr    error
	stats       attachOutputStats
}

func newAttachDataWriter(cfg attachDataWriterConfig) *boundedAttachDataWriter {
	mode := cfg.Mode
	if mode == "" {
		mode = attachOutputRaw
	}

	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultAttachOutputQueueMaxBytes
	}

	maxChunks := cfg.MaxChunks
	if maxChunks <= 0 {
		maxChunks = defaultAttachOutputQueueMaxChunks
	}

	coalesceBytes := cfg.CoalesceBytes
	if coalesceBytes <= 0 {
		coalesceBytes = defaultAttachOutputCoalesceBytes
	}

	writeTimeout := cfg.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultAttachOutputWriteTimeout
	}

	writeFrame := cfg.writeFrame
	if writeFrame == nil && cfg.Writer != nil {
		writeFrame = func(payload []byte) error {
			return cfg.Writer.WriteFrameWithDeadline(protocol.ChannelData, payload, writeTimeout)
		}
	}

	if writeFrame == nil {
		writeFrame = func([]byte) error { return nil }
	}

	closeConn := cfg.closeConn
	if closeConn == nil && cfg.Conn != nil {
		closeConn = cfg.Conn.Close
	}

	if closeConn == nil {
		closeConn = func() error { return nil }
	}

	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	w := &boundedAttachDataWriter{
		sessionID:     cfg.SessionID,
		log:           log,
		mode:          mode,
		maxBytes:      maxBytes,
		maxChunks:     maxChunks,
		coalesceBytes: coalesceBytes,
		writeFrame:    writeFrame,
		closeConn:     closeConn,
		done:          make(chan struct{}),
	}
	w.cond = sync.NewCond(&w.mu)

	go w.run()

	return w
}

func (w *boundedAttachDataWriter) Write(p []byte) (int, error) {
	if w.mode == attachOutputCoalesced {
		w.enqueueCoalesced(len(p))

		return len(p), nil
	}

	if len(p) == 0 {
		return 0, nil
	}

	w.mu.Lock()
	if w.closed {
		w.recordDroppedLocked(len(p))
		w.mu.Unlock()

		return len(p), nil
	}
	w.mu.Unlock()

	payload := append([]byte(nil), p...)

	overflow := false
	droppedBytes := 0

	w.mu.Lock()
	if w.closed {
		w.recordDroppedLocked(len(payload))
		w.mu.Unlock()

		return len(p), nil
	}

	if len(w.queue) >= w.maxChunks || w.queuedBytes+len(payload) > w.maxBytes {
		droppedFrames := len(w.queue) + 1
		droppedBytes = w.queuedBytes + len(payload)
		w.closed = true
		w.overflowed = true
		w.stats.droppedFrames += int64(droppedFrames)
		w.stats.droppedBytes += int64(droppedBytes)
		w.queue = nil
		w.queuedBytes = 0
		overflow = true

		w.cond.Broadcast()
	} else {
		if w.tryCoalesceRawLocked(payload) {
			w.stats.coalesced++
		} else {
			w.queue = append(w.queue, payload)
		}

		w.queuedBytes += len(payload)
		w.stats.enqueuedFrames++
		w.stats.enqueuedBytes += int64(len(payload))
		w.cond.Signal()
	}
	w.mu.Unlock()

	if overflow {
		w.log.Warn("attached output queue full; disconnecting slow client",
			"session", w.sessionID,
			"mode", string(w.mode),
			"max_bytes", w.maxBytes,
			"max_chunks", w.maxChunks,
			"dropped_bytes", droppedBytes)
		_ = w.closeConn()
	}

	return len(p), nil
}

func (w *boundedAttachDataWriter) enqueueCoalesced(bytes int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		w.recordDroppedLocked(bytes)

		return
	}

	if len(w.queue) > 0 {
		w.stats.coalesced++

		return
	}

	w.queue = append(w.queue, nil)
	w.stats.enqueuedFrames++
	w.cond.Signal()
}

func (w *boundedAttachDataWriter) tryCoalesceRawLocked(payload []byte) bool {
	if w.mode != attachOutputRaw || len(w.queue) == 0 {
		return false
	}

	last := w.queue[len(w.queue)-1]
	if len(last)+len(payload) > w.coalesceBytes {
		return false
	}

	w.queue[len(w.queue)-1] = append(last, payload...)

	return true
}

func (w *boundedAttachDataWriter) recordDroppedLocked(bytes int) {
	if !w.overflowed {
		return
	}

	w.stats.droppedFrames++
	w.stats.droppedBytes += int64(bytes)
}

func (w *boundedAttachDataWriter) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		w.queue = nil
		w.queuedBytes = 0
		w.cond.Broadcast()
	}
	w.mu.Unlock()
}

func (w *boundedAttachDataWriter) wait() {
	<-w.done
}

func (w *boundedAttachDataWriter) snapshotStats() attachOutputStats {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.stats
}

func (w *boundedAttachDataWriter) run() {
	defer close(w.done)

	for {
		payload, ok := w.nextPayload()
		if !ok {
			w.mu.Lock()
			stats := w.stats
			overflowed := w.overflowed
			writeErr := w.writeErr
			w.mu.Unlock()

			w.log.Debug("attached output writer stopped",
				"session", w.sessionID,
				"mode", string(w.mode),
				"enqueued_frames", stats.enqueuedFrames,
				"enqueued_bytes", stats.enqueuedBytes,
				"written_frames", stats.writtenFrames,
				"written_bytes", stats.writtenBytes,
				"coalesced", stats.coalesced,
				"dropped_frames", stats.droppedFrames,
				"dropped_bytes", stats.droppedBytes,
				"overflowed", overflowed,
				"write_error", writeErr)

			return
		}

		if err := w.writeFrame(payload); err != nil {
			w.mu.Lock()
			alreadyClosed := w.closed
			w.closed = true
			w.writeErr = err
			w.queue = nil
			w.queuedBytes = 0
			w.cond.Broadcast()
			w.mu.Unlock()

			if !alreadyClosed {
				w.log.Warn("attached output write failed; disconnecting client",
					"session", w.sessionID,
					"mode", string(w.mode),
					"err", err)
			}

			_ = w.closeConn()

			continue
		}

		w.mu.Lock()
		w.stats.writtenFrames++
		w.stats.writtenBytes += int64(len(payload))
		w.mu.Unlock()
	}
}

func (w *boundedAttachDataWriter) nextPayload() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for len(w.queue) == 0 && !w.closed {
		w.cond.Wait()
	}

	if len(w.queue) == 0 {
		return nil, false
	}

	payload := w.queue[0]
	copy(w.queue, w.queue[1:])
	w.queue[len(w.queue)-1] = nil
	w.queue = w.queue[:len(w.queue)-1]
	w.queuedBytes -= len(payload)

	return payload, true
}

// gatedDataWriter is used only for terminal-owned attach seeding. It suppresses
// live output until the initial snapshot reaches the client, then emits at most
// one repaint hint for any output that arrived while the seed was in flight.
type gatedDataWriter struct {
	mu        sync.Mutex
	target    io.Writer
	released  bool
	discarded bool
	dirty     bool
}

func newGatedDataWriter(target io.Writer) *gatedDataWriter {
	return &gatedDataWriter{target: target}
}

func (w *gatedDataWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if !w.released {
		w.dirty = true
		w.mu.Unlock()

		return len(p), nil
	}

	target := w.target
	discarded := w.discarded
	w.mu.Unlock()

	if discarded {
		return len(p), nil
	}

	n, err := target.Write(p)
	if n < len(p) && err == nil {
		return len(p), nil
	}

	return len(p), err
}

func (w *gatedDataWriter) Release() error {
	w.mu.Lock()
	if w.released {
		w.mu.Unlock()

		return nil
	}

	target := w.target
	dirty := w.dirty
	discarded := w.discarded
	w.released = true
	w.dirty = false
	w.mu.Unlock()

	if discarded || !dirty {
		return nil
	}

	_, err := target.Write(nil)

	return err
}

func (w *gatedDataWriter) Discard() {
	w.mu.Lock()
	w.released = true
	w.discarded = true
	w.dirty = false
	w.mu.Unlock()
}

func (w *gatedDataWriter) Close() {
	w.Discard()

	if closer, ok := w.target.(interface{ Close() }); ok {
		closer.Close()
	}
}
