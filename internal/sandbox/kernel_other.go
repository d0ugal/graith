//go:build !linux

package sandbox

import "errors"

// kernelRelease is only meaningful on Linux (for Landlock ABI detection). On
// other platforms landlockState returns landlockNotApplicable before this is
// reached, so this exists only to satisfy the compiler.
func kernelRelease() (string, error) {
	return "", errors.New("kernel release is only available on Linux")
}
