//go:build libghostty && cgo && linux

package pty

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/d0ugal/graith/internal/executablepin"
)

func pinRunningGhosttyExecutable() (*ghosttyPinnedImage, error) {
	return pinGhosttyExecutable("/proc/self/exe")
}

func pinGhosttyExecutable(path string) (*ghosttyPinnedImage, error) {
	source, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 ||
		info.Mode().Perm()&0o022 != 0 {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("pinned helper image metadata is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) {
		return nil, errors.New("pinned helper image has unexpected owner")
	}
	wantDigest, err := ghosttyLinuxFileDigest(source, info.Size())
	if err != nil {
		return nil, err
	}

	retained, err := executablepin.SealedCopy(source, info.Size(), "graith-helper-image")
	if err != nil {
		return nil, err
	}
	retainedInfo, err := retained.Stat()
	if err != nil || !retainedInfo.Mode().IsRegular() || retainedInfo.Mode().Perm()&0o111 == 0 ||
		retainedInfo.Size() != info.Size() {
		_ = retained.Close()
		return nil, errors.New("retained helper image metadata is unsafe")
	}
	retainedDigest, err := ghosttyLinuxFileDigest(retained, retainedInfo.Size())
	if err != nil || retainedDigest != wantDigest {
		_ = retained.Close()
		return nil, errors.New("retained helper image content differs")
	}
	pinned := &ghosttyPinnedImage{
		file: retained,
		path: "/proc/self/fd/3",
	}
	pinned.prepare = func() error { return nil }
	pinned.validate = func() error {
		return executablepin.Validate(retained, retainedInfo.Size())
	}
	pinned.cleanup = retained.Close

	return pinned, nil
}

func ghosttyLinuxFileDigest(file *os.File, size int64) ([sha256.Size]byte, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.NewSectionReader(file, 0, size)); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}
