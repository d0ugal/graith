//go:build !linux

package sandbox

import "runtime"

func landlockState() landlockInfo {
	return landlockInfo{kind: landlockNotApplicable, detail: runtime.GOOS + " uses Seatbelt, not Landlock"}
}
