//go:build libghostty && cgo && darwin

package pty

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

func pinRunningGhosttyExecutable() (*ghosttyPinnedImage, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return pinGhosttyExecutable(path)
}

func pinGhosttyExecutable(path string) (_ *ghosttyPinnedImage, returnErr error) {
	source, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	keepSource := false
	defer func() {
		if !keepSource {
			returnErr = errors.Join(returnErr, source.Close())
		}
	}()

	sourceInfo, err := source.Stat()
	if err != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Mode().Perm()&0o111 == 0 ||
		sourceInfo.Mode().Perm()&0o022 != 0 {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("pinned helper image metadata is unsafe")
	}
	sourceStat, ok := sourceInfo.Sys().(*syscall.Stat_t)
	if !ok || (int(sourceStat.Uid) != os.Geteuid() && sourceStat.Uid != 0) {
		return nil, errors.New("pinned helper image has unexpected owner")
	}

	var mu sync.Mutex
	var retainedDir string
	var retainedInfo os.FileInfo
	var retainedDigest [sha256.Size]byte
	var retainedSize int64
	pinned := &ghosttyPinnedImage{file: source}

	releasePathLocked := func() error {
		var result error
		if pinned.path != "" {
			if err := os.Remove(pinned.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			} else {
				pinned.path = ""
				retainedInfo = nil
				retainedDigest = [sha256.Size]byte{}
				retainedSize = 0
			}
		}
		if pinned.path == "" && retainedDir != "" {
			if err := os.Remove(retainedDir); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			} else {
				retainedDir = ""
			}
		}
		return result
	}

	pinned.prepare = func() error {
		mu.Lock()
		defer mu.Unlock()
		if pinned.path != "" {
			return validateGhosttyRetainedImage(
				source, sourceInfo, retainedDir, pinned.path, retainedInfo, retainedSize, retainedDigest,
			)
		}
		if retainedDir != "" {
			if err := releasePathLocked(); err != nil {
				return err
			}
		}

		dirPattern := fmt.Sprintf(".graith-helper-image-%d-", os.Getpid())
		dir, err := os.MkdirTemp("", dirPattern)
		if err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
		retainedPath := filepath.Join(dir, "graith-helper")

		if _, err := source.Seek(0, io.SeekStart); err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
		destination, err := os.OpenFile(retainedPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
		if err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
		hasher := sha256.New()
		copied, copyErr := io.Copy(io.MultiWriter(destination, hasher), source)
		if copyErr == nil {
			copyErr = destination.Sync()
		}
		closeErr := destination.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.RemoveAll(dir)
			return errors.Join(copyErr, closeErr)
		}
		var digest [sha256.Size]byte
		copy(digest[:], hasher.Sum(nil))

		dirFile, err := os.Open(dir)
		if err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
		syncErr := dirFile.Sync()
		closeErr = dirFile.Close()
		if syncErr != nil || closeErr != nil {
			_ = os.RemoveAll(dir)
			return errors.Join(syncErr, closeErr)
		}

		info, err := os.Lstat(retainedPath)
		if err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
		if err := validateGhosttyRetainedImage(source, sourceInfo, dir, retainedPath, info, copied, digest); err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
		retainedDir = dir
		retainedInfo = info
		retainedSize = copied
		retainedDigest = digest
		pinned.path = retainedPath
		return nil
	}
	pinned.validate = func() error {
		mu.Lock()
		defer mu.Unlock()
		return validateGhosttyRetainedImage(
			source, sourceInfo, retainedDir, pinned.path, retainedInfo, retainedSize, retainedDigest,
		)
	}
	pinned.releasePath = func() error {
		mu.Lock()
		defer mu.Unlock()
		return releasePathLocked()
	}
	pinned.cleanup = func() error {
		mu.Lock()
		defer mu.Unlock()
		return errors.Join(releasePathLocked(), source.Close())
	}
	keepSource = true

	return pinned, nil
}

func validateGhosttyRetainedImage(
	source *os.File,
	sourceInfo os.FileInfo,
	dir, path string,
	retainedInfo os.FileInfo,
	wantSize int64,
	wantDigest [sha256.Size]byte,
) error {
	currentSource, err := source.Stat()
	if err != nil {
		return err
	}
	if !currentSource.Mode().IsRegular() || !os.SameFile(sourceInfo, currentSource) ||
		currentSource.Mode().Perm()&0o111 == 0 || currentSource.Mode().Perm()&0o022 != 0 {
		return errors.New("pinned helper image identity changed")
	}
	if dir == "" || path == "" || retainedInfo == nil {
		return errors.New("retained helper image is unavailable")
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if err := validateGhosttyRetainedDirectory(dirInfo); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || current.Mode().Perm()&0o222 != 0 ||
		current.Mode().Perm()&0o111 == 0 || !os.SameFile(retainedInfo, current) || current.Size() != wantSize {
		return errors.New("retained helper image identity or mode changed")
	}
	stat, ok := current.Sys().(*syscall.Stat_t)
	if !ok || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) {
		return errors.New("retained helper image has unexpected owner")
	}
	digest, size, err := ghosttyFileDigest(path)
	if err != nil || size != wantSize || digest != wantDigest {
		return errors.New("retained helper image content changed")
	}
	sourceDigest, sourceSize, err := ghosttyOpenFileDigest(source, currentSource.Size())
	if err != nil || sourceSize != wantSize || sourceDigest != wantDigest {
		return errors.New("pinned and retained helper images differ")
	}
	return nil
}

func ghosttyFileDigest(path string) ([sha256.Size]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return [sha256.Size]byte{}, 0, err
	}
	return ghosttyOpenFileDigest(file, info.Size())
}

func ghosttyOpenFileDigest(file *os.File, size int64) ([sha256.Size]byte, int64, error) {
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.NewSectionReader(file, 0, size))
	if err != nil {
		return [sha256.Size]byte{}, written, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, written, nil
}

func validateGhosttyRetainedDirectory(info os.FileInfo) error {
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("retained helper directory is not private")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("retained helper directory has unexpected owner")
	}
	return nil
}
