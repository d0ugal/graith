package pty

import (
	"bytes"
	"fmt"
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
	n, err := s.file.Write(data)
	s.written += int64(n)
	return n, err
}

func (s *Scrollback) Tail(lines int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	if lines <= 0 {
		return data, nil
	}
	idx := len(data)
	count := 0
	for idx > 0 && count < lines {
		idx--
		if data[idx] == '\n' && idx < len(data)-1 {
			count++
		}
	}
	if idx > 0 {
		idx++
	}
	return bytes.Clone(data[idx:]), nil
}

func (s *Scrollback) Close() error { return s.file.Close() }

func (s *Scrollback) Remove() error {
	s.Close()
	return os.Remove(s.path)
}
