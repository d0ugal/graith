package pty

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// Scrollback is an append-only log file. path is immutable after construction,
// so Tail/TailBytes read it lock-free via independent file descriptors.
type Scrollback struct {
	mu        sync.RWMutex
	file      *os.File
	path      string
	maxSize   int64
	written   int64
	saturated bool
	log       *slog.Logger
}

func NewScrollback(path string, maxSize int64) (*Scrollback, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open scrollback: %w", err)
	}

	info, _ := f.Stat()

	written := int64(0)
	if info != nil {
		written = info.Size()
	}

	return &Scrollback{file: f, path: path, maxSize: maxSize, written: written, log: slog.Default()}, nil
}

// SetLogger routes the scrollback writer's diagnostics to the daemon's logger.
// The daemon never calls slog.SetDefault and runs with stderr sent to
// /dev/null, so without this the writer's logs would be discarded (issue
// #1087). A nil logger is ignored (keeps the slog.Default() fallback set at
// construction).
func (s *Scrollback) SetLogger(log *slog.Logger) {
	if log == nil {
		return
	}

	s.mu.Lock()
	s.log = log
	s.mu.Unlock()
}

func (s *Scrollback) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.maxSize > 0 && s.written >= s.maxSize {
		if !s.saturated {
			s.saturated = true
			s.log.Warn("scrollback full, further output will not be recorded", "path", s.path, "max_size", s.maxSize)
		}

		return len(data), nil
	}

	n, err := s.file.Write(data)
	s.written += int64(n)

	return n, err
}

func (s *Scrollback) Tail(lines int) ([]byte, error) {
	return TailFile(s.path, lines)
}

// TailFile returns the last `lines` lines from a scrollback file on disk
// without a live Scrollback. It is used to read logs for stopped sessions
// whose live PTY has already been torn down. A missing file is returned as
// an error; an existing but empty file returns (nil, nil).
func TailFile(path string, lines int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := info.Size()
	if size == 0 {
		return nil, nil
	}

	if lines <= 0 {
		data := make([]byte, size)
		_, err := io.ReadFull(f, data)

		return data, err
	}

	const chunkSize = 8192

	count := 0
	remaining := size
	// Chunks are collected in reverse file order (last chunk first).
	var chunks [][]byte

	for remaining > 0 {
		readSize := min(int64(chunkSize), remaining)
		remaining -= readSize

		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, remaining); err != nil {
			return nil, err
		}

		chunks = append(chunks, chunk)

		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] == '\n' && remaining+int64(i) < size-1 {
				count++
				if count >= lines {
					parts := make([][]byte, 0, len(chunks))

					parts = append(parts, chunk[i+1:])
					for j := len(chunks) - 2; j >= 0; j-- {
						parts = append(parts, chunks[j])
					}

					return bytes.Join(parts, nil), nil
				}
			}
		}
	}

	// Fewer than requested lines — reverse and return everything.
	for left, right := 0, len(chunks)-1; left < right; left, right = left+1, right-1 {
		chunks[left], chunks[right] = chunks[right], chunks[left]
	}

	return bytes.Join(chunks, nil), nil
}

// TailBytes returns up to maxBytes from the end of the scrollback file.
func (s *Scrollback) TailBytes(maxBytes int64) ([]byte, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := info.Size()
	if size == 0 {
		return nil, nil
	}

	readSize := size
	if maxBytes > 0 && readSize > maxBytes {
		readSize = maxBytes
	}

	data := make([]byte, readSize)

	_, err = f.ReadAt(data, size-readSize)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (s *Scrollback) Stats() (written, maxSize int64, saturated bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.written, s.maxSize, s.saturated
}

func (s *Scrollback) Close() error { return s.file.Close() }

func (s *Scrollback) Remove() error {
	_ = s.Close()
	return os.Remove(s.path)
}
