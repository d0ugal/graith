//go:build linux

package daemon

func (p *upgradeTargetPin) retainPlatformCopy() error {
	return p.retainLinuxCopy()
}
