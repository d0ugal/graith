package pty

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
)

type Scrollback struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	maxSize int64
	written int64
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
	return &Scrollback{file: f, path: path, maxSize: maxSize, written: written}, nil
}

func (s *Scrollback) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxSize > 0 && s.written >= s.maxSize {
		return len(data), nil
	}
	n, err := s.file.Write(data)
	s.written += int64(n)
	return n, err
}

func (s *Scrollback) Tail(lines int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

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
	var buf []byte

	for remaining > 0 {
		readSize := min(int64(chunkSize), remaining)
		remaining -= readSize

		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, remaining); err != nil {
			return nil, err
		}

		buf = append(chunk, buf...)

		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] == '\n' && remaining+int64(i) < size-1 {
				count++
				if count >= lines {
					return bytes.Clone(buf[i+1:]), nil
				}
			}
		}
	}

	return bytes.Clone(buf), nil
}

func (s *Scrollback) Close() error { return s.file.Close() }

func (s *Scrollback) Remove() error {
	s.Close()
	return os.Remove(s.path)
}
