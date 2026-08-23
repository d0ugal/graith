//go:build !linux && !darwin

package daemon

func (p *upgradeTargetPin) retainPlatformCopy() error {
	return p.retainDarwinCopy()
}
