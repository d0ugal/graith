//go:build linux

package daemon

import (
	"errors"
	"fmt"

	"github.com/d0ugal/graith/internal/executablepin"
)

func (p *upgradeTargetPin) retainPlatformCopy() error {
	return p.retainLinuxCopy()
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
