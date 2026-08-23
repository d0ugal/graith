//go:build linux

package sandbox

func landlockState() landlockInfo {
	rel, err := kernelRelease()
	if err != nil {
		return landlockInfo{kind: landlockNotEnforced, detail: "could not read kernel release: " + err.Error()}
	}

	return classifyLandlock(rel)
}
