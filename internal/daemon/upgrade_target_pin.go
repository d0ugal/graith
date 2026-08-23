package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	if err := pin.retainPlatformCopy(); err != nil {
		return nil, err
	}

	keep = true

	return pin, nil
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

	info, err := os.Lstat(dir) // #nosec G703 -- retained dir basename is validated before stat.
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("upgrade target retained directory is unsafe")
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("upgrade target retained directory owner is unsafe")
	}

	return os.RemoveAll(dir) // #nosec G703 -- retained dir mode and owner are validated above.
}
