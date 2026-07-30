package daemon

import (
	"fmt"
	"os"
	"sync"
)

type rotatingLogFile struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
}

func openRotatingLogFile(path string, maxBytes int64, maxBackups int) (*rotatingLogFile, error) {
	if maxBackups < 0 {
		maxBackups = 0
	}

	r := &rotatingLogFile{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
	}

	if maxBytes > 0 {
		if info, err := os.Stat(path); err == nil && info.Size() >= maxBytes {
			if err := rotateLogPath(path, maxBackups); err != nil {
				return nil, err
			}
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}

	if err := r.open(); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *rotatingLogFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", r.path, err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return fmt.Errorf("stat %s: %w", r.path, err)
	}

	r.file = f
	r.size = info.Size()

	return nil
}

func (r *rotatingLogFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}

	if r.maxBytes > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}

	n, err := r.file.Write(p)
	r.size += int64(n)

	return n, err
}

func (r *rotatingLogFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		return nil
	}

	err := r.file.Close()
	r.file = nil

	return err
}

func (r *rotatingLogFile) rotateLocked() error {
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			return fmt.Errorf("close %s before rotate: %w", r.path, err)
		}

		r.file = nil
	}

	if err := rotateLogPath(r.path, r.maxBackups); err != nil {
		return err
	}

	return r.open()
}

func rotateLogPath(path string, maxBackups int) error {
	if maxBackups <= 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old log %s: %w", path, err)
		}

		return nil
	}

	if err := os.Remove(logBackupPath(path, maxBackups)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove oldest log backup: %w", err)
	}

	for i := maxBackups - 1; i >= 1; i-- {
		src := logBackupPath(path, i)
		dst := logBackupPath(path, i+1)

		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate log backup %s to %s: %w", src, dst, err)
		}
	}

	if err := os.Rename(path, logBackupPath(path, 1)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate log %s: %w", path, err)
	}

	return nil
}

func logBackupPath(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}
