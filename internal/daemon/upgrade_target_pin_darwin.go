//go:build darwin

package daemon

func (p *upgradeTargetPin) retainPlatformCopy() error {
	return p.retainDarwinCopy()
}
