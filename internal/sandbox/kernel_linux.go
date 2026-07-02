//go:build linux

package sandbox

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// kernelRelease returns the running kernel release string (e.g. "6.1.0-31").
func kernelRelease() (string, error) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", err
	}

	return string(bytes.TrimRight(u.Release[:], "\x00")), nil
}
