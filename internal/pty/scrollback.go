package pty

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Scrollback is an append-only log file. Live reads use the exact open inode;
// path is retained for reversible pre-exec identity validation and deletion.
type Scrollback struct {
	mu        sync.RWMutex
	file      *os.File
	path      string
	dev       uint64
	ino       uint64
	maxSize   int64
	written   int64
	saturated bool
	closed    bool
	log       *slog.Logger
}

func validateScrollbackDescriptor(fd int) (unix.Stat_t, error) {
	var descriptor unix.Stat_t
	if err := unix.Fstat(fd, &descriptor); err != nil || descriptor.Mode&unix.S_IFMT != unix.S_IFREG ||
		descriptor.Uid != uint32(os.Geteuid()) || descriptor.Mode&0o077 != 0 { //nolint:gosec // G115: geteuid returns the kernel's non-negative uid_t value
		return unix.Stat_t{}, errors.New("scrollback descriptor is not a private regular file")
	}

	return descriptor, nil
}

// ValidateTransferredScrollbackFD checks a handoff descriptor without taking
// ownership. The daemon uses it before atomically moving the PTY/log pair out
// of its post-exec ownership guard.
func ValidateTransferredScrollbackFD(fd uintptr) error {
	if _, err := validateScrollbackDescriptor(int(fd)); err != nil {
		return err
	}

	flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil || flags&unix.O_APPEND == 0 || flags&unix.O_ACCMODE != unix.O_RDWR {
		return errors.New("inherited scrollback descriptor is not a readable append writer")
	}

	return nil
}

// AdoptScrollback takes ownership of an inherited append writer without
// reopening its pathname. Path identity is validated reversibly before exec;
// after exec the inherited inode remains authoritative if the path changes.
func AdoptScrollback(fd uintptr, path string, maxSize int64) (*Scrollback, error) {
	descriptor, err := validateScrollbackDescriptor(int(fd))
	if err != nil {
		return nil, errors.New("inherited scrollback descriptor is not a private regular file")
	}

	if err := ValidateTransferredScrollbackFD(fd); err != nil {
		return nil, errors.New("inherited scrollback descriptor is not a readable append writer")
	}

	f := os.NewFile(fd, "scrollback-adopted")
	if f == nil {
		return nil, errors.New("invalid inherited scrollback descriptor")
	}

	return &Scrollback{
		file: f, path: path,
		dev: uint64(descriptor.Dev), ino: descriptor.Ino, //nolint:gosec,unconvert,nolintlint // G115/unconvert are platform-exclusive because Dev is signed on Darwin and unsigned on Linux.
		maxSize: maxSize, written: descriptor.Size, log: slog.Default(),
	}, nil
}

func NewScrollback(path string, maxSize int64) (*Scrollback, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, errors.New("open scrollback")
	}

	descriptor, validateErr := validateScrollbackDescriptor(fd)
	if validateErr != nil {
		_ = unix.Close(fd)

		return nil, validateErr
	}

	f := os.NewFile(uintptr(fd), "scrollback")
	if f == nil {
		_ = unix.Close(fd)

		return nil, errors.New("open scrollback")
	}

	return &Scrollback{
		file: f, path: path,
		dev: uint64(descriptor.Dev), ino: descriptor.Ino, //nolint:gosec,unconvert,nolintlint // G115/unconvert are platform-exclusive because Dev is signed on Darwin and unsigned on Linux.
		maxSize: maxSize, written: descriptor.Size, log: slog.Default(),
	}, nil
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

	if s.closed || s.file == nil {
		return 0, os.ErrClosed
	}

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

// DuplicateFD returns a close-on-exec duplicate owned by the caller.
func (s *Scrollback) DuplicateFD() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed || s.file == nil {
		return -1, os.ErrClosed
	}

	fd, err := unix.FcntlInt(s.file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return -1, fmt.Errorf("duplicate scrollback descriptor: %w", err)
	}

	return fd, nil
}

// ValidatePathIdentity proves the configured pathname still names this exact
// private writer. Upgrade calls it before exec, where refusal is reversible.
func (s *Scrollback) ValidatePathIdentity() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed || s.file == nil {
		return os.ErrClosed
	}

	descriptor, err := validateScrollbackDescriptor(int(s.file.Fd()))
	if err != nil {
		return errors.New("scrollback descriptor is not a private regular file")
	}

	var pathname unix.Stat_t
	if err := unix.Lstat(s.path, &pathname); err != nil || pathname.Mode&unix.S_IFMT != unix.S_IFREG ||
		pathname.Dev != descriptor.Dev || pathname.Ino != descriptor.Ino {
		return errors.New("scrollback pathname identity changed")
	}

	flags, err := unix.FcntlInt(s.file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_APPEND == 0 || flags&unix.O_ACCMODE != unix.O_RDWR {
		return errors.New("scrollback descriptor is not a readable append writer")
	}

	return nil
}

func (s *Scrollback) Tail(lines int) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed || s.file == nil {
		return nil, os.ErrClosed
	}

	return tailFile(s.file, lines)
}

// TailFile returns the last `lines` lines from a scrollback file on disk
// without a live Scrollback. It is used to read logs for stopped sessions
// whose live PTY has already been torn down. A missing file is returned as
// an error; an existing but empty file returns (nil, nil).
func TailFile(path string, lines int) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}

	if _, err := validateScrollbackDescriptor(fd); err != nil {
		_ = unix.Close(fd)

		return nil, err
	}

	f := os.NewFile(uintptr(fd), "scrollback-tail")
	if f == nil {
		_ = unix.Close(fd)

		return nil, errors.New("open scrollback tail")
	}
	defer func() { _ = f.Close() }()

	return tailFile(f, lines)
}

func tailFile(f *os.File, lines int) ([]byte, error) {
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
		_, err := f.ReadAt(data, 0)

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
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed || s.file == nil {
		return nil, os.ErrClosed
	}

	info, err := s.file.Stat()
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

	_, err = s.file.ReadAt(data, size-readSize)
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

func (s *Scrollback) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	if s.file == nil {
		return nil
	}

	err := s.file.Close()
	if errors.Is(err, syscall.EBADF) {
		return nil
	}

	return err
}

func (s *Scrollback) Remove() error {
	_ = s.Close()

	var pathname unix.Stat_t
	if err := unix.Lstat(s.path, &pathname); err != nil {
		return errors.New("scrollback pathname is unavailable")
	}

	if pathname.Mode&unix.S_IFMT != unix.S_IFREG ||
		uint64(pathname.Dev) != s.dev || pathname.Ino != s.ino { //nolint:gosec,unconvert,nolintlint // G115/unconvert are platform-exclusive because Dev is signed on Darwin and unsigned on Linux.
		return errors.New("scrollback pathname identity changed")
	}

	if err := os.Remove(s.path); err != nil {
		return errors.New("remove scrollback")
	}

	return nil
}
