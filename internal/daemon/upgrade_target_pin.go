package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/d0ugal/graith/internal/executablepin"
)

type upgradeTargetPin struct {
	file        *os.File
	info        os.FileInfo
	digest      string
	original    string
	execPath    string
	retainedDir string
	sealed      bool
}

func pinUpgradeTarget(path string) (_ *upgradeTargetPin, returnErr error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	keep := false
	defer func() {
		if !keep {
			returnErr = errors.Join(returnErr, file.Close())
		}
	}()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("upgrade target metadata is unsafe")
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) {
		return nil, errors.New("upgrade target owner is unsafe")
	}

	pathInfo, err := os.Stat(path)
	if err != nil || !os.SameFile(info, pathInfo) {
		return nil, errors.New("upgrade target changed while it was opened")
	}

	digest, err := digestUpgradeTargetFile(file, info.Size())
	if err != nil {
		return nil, err
	}

	pin := &upgradeTargetPin{file: file, info: info, digest: digest, original: path}
	if runtime.GOOS == "linux" {
		if err := pin.retainLinuxCopy(); err != nil {
			return nil, err
		}
	} else {
		if err := pin.retainDarwinCopy(); err != nil {
			return nil, err
		}
	}

	keep = true

	return pin, nil
}

func (p *upgradeTargetPin) retainLinuxCopy() error {
	retained, err := executablepin.SealedCopy(p.file, p.info.Size(), "graith-upgrade-target")
	if err != nil {
		return err
	}

	retainedInfo, err := retained.Stat()
	if err != nil || !retainedInfo.Mode().IsRegular() || retainedInfo.Mode().Perm()&0o111 == 0 || retainedInfo.Size() != p.info.Size() {
		_ = retained.Close()
		return errors.New("retained upgrade target metadata is unsafe")
	}

	retainedDigest, err := digestUpgradeTargetFile(retained, retainedInfo.Size())
	if err != nil || retainedDigest != p.digest {
		_ = retained.Close()
		return errors.New("retained upgrade target content differs")
	}

	if err := p.file.Close(); err != nil {
		_ = retained.Close()
		return err
	}

	p.file = retained
	p.info = retainedInfo
	p.execPath = fmt.Sprintf("/proc/self/fd/%d", retained.Fd())
	p.sealed = true

	return nil
}

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
	dir, err := os.Open(path)
	if err != nil {
		return err
	}

	return errors.Join(dir.Sync(), dir.Close())
}

func (p *upgradeTargetPin) validate() error {
	info, err := p.file.Stat()
	if err != nil || !os.SameFile(p.info, info) || info.Size() != p.info.Size() || info.Mode() != p.info.Mode() {
		return errors.New("opened upgrade target identity changed")
	}

	if p.sealed {
		return executablepin.Validate(p.file, p.info.Size())
	}

	digest, err := digestUpgradeTargetFile(p.file, info.Size())
	if err != nil || digest != p.digest {
		return errors.New("opened upgrade target content changed")
	}

	if p.retainedDir != "" {
		dirInfo, err := os.Lstat(p.retainedDir)
		if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
			return errors.New("retained upgrade target directory changed")
		}

		dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
		if !ok || int(dirStat.Uid) != os.Geteuid() {
			return errors.New("retained upgrade target directory owner changed")
		}

		retainedInfo, err := os.Lstat(p.execPath)
		if err != nil || !retainedInfo.Mode().IsRegular() || retainedInfo.Mode().Perm() != 0o500 ||
			retainedInfo.Size() != info.Size() || !os.SameFile(p.info, retainedInfo) {
			return errors.New("retained upgrade target identity changed")
		}

		retainedDigest, err := digestFile(p.execPath)
		if err != nil || retainedDigest != p.digest {
			return errors.New("retained upgrade target content changed")
		}
	}

	return nil
}

// validateFinal performs only descriptor-local metadata/seal checks. The full
// content hash and retained-path validation must already have succeeded before
// manager, persistence, and ForkLock are acquired; doing filesystem/hash I/O
// inside that final barrier can deadlock unrelated process starts on Darwin.
func (p *upgradeTargetPin) validateFinal() error {
	info, err := p.file.Stat()
	if err != nil || !os.SameFile(p.info, info) || info.Size() != p.info.Size() || info.Mode() != p.info.Mode() {
		return errors.New("opened upgrade target identity changed")
	}

	if p.sealed {
		return executablepin.Validate(p.file, p.info.Size())
	}

	return nil
}

func (p *upgradeTargetPin) probeCommand(ctx context.Context, args ...string) *exec.Cmd {
	path := p.execPath

	cmd := exec.CommandContext(ctx, path, args...)
	if runtime.GOOS == "linux" {
		cmd.Path = "/proc/self/fd/3"
		cmd.ExtraFiles = []*os.File{p.file}
	}

	return cmd
}

func (p *upgradeTargetPin) close() error {
	var result error
	if p.retainedDir != "" {
		result = errors.Join(result, os.RemoveAll(p.retainedDir))
		p.retainedDir = ""
	}

	if p.file != nil {
		result = errors.Join(result, p.file.Close())
		p.file = nil
	}

	return result
}

func digestUpgradeTargetFile(file *os.File, size int64) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.NewSectionReader(file, 0, size)); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func cleanupRetainedUpgradeTarget(path string) error {
	if path == "" {
		return nil
	}

	dir := filepath.Dir(path)
	if !strings.HasPrefix(filepath.Base(dir), ".graith-upgrade-target-") || filepath.Base(path) != "graith" {
		return errors.New("upgrade target retained path is unsafe")
	}

	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("upgrade target retained directory is unsafe")
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("upgrade target retained directory owner is unsafe")
	}

	return os.RemoveAll(dir)
}
