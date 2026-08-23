//go:build !linux

package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (p *upgradeTargetPin) retainDarwinCopy() error {
	dir, path, err := p.writePrivateCopy()
	if err != nil {
		return err
	}

	retainedInfo, err := os.Lstat(path)
	if err != nil || !retainedInfo.Mode().IsRegular() || retainedInfo.Mode().Perm() != 0o500 || retainedInfo.Size() != p.info.Size() {
		_ = os.RemoveAll(dir)
		return errors.New("retained upgrade target metadata is unsafe")
	}

	digest, err := digestFile(path)
	if err != nil || digest != p.digest {
		_ = os.RemoveAll(dir)
		return errors.New("retained upgrade target content differs")
	}

	retained, err := os.Open(path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}

	openedInfo, err := retained.Stat()
	if err != nil || !os.SameFile(retainedInfo, openedInfo) {
		_ = retained.Close()
		_ = os.RemoveAll(dir)

		return errors.New("retained upgrade target changed while it was opened")
	}

	if err := p.file.Close(); err != nil {
		_ = retained.Close()
		_ = os.RemoveAll(dir)

		return err
	}

	p.file = retained
	p.info = openedInfo
	p.retainedDir = dir
	p.execPath = path

	return nil
}

func (p *upgradeTargetPin) writePrivateCopy() (string, string, error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf(".graith-upgrade-target-%d-", os.Getpid()))
	if err != nil {
		return "", "", err
	}

	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: this is a private directory, so owner execute is required
		_ = os.RemoveAll(dir)
		return "", "", err
	}

	path := filepath.Join(dir, "graith")

	destination, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500) //nolint:gosec // G302: retained target must remain owner-executable
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}

	if err := destination.Chmod(0o500); err != nil {
		_ = destination.Close()
		_ = os.RemoveAll(dir)

		return "", "", err
	}

	written, copyErr := io.Copy(destination, io.NewSectionReader(p.file, 0, p.info.Size()))
	if copyErr == nil && written != p.info.Size() {
		copyErr = io.ErrShortWrite
	}

	if copyErr == nil {
		copyErr = destination.Sync()
	}

	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(dir)
		return "", "", errors.Join(copyErr, closeErr)
	}

	if err := syncDirectory(dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}

	return dir, path, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path) // #nosec G703 -- path is a private pin directory created by this package.
	if err != nil {
		return err
	}

	return errors.Join(dir.Sync(), dir.Close())
}
