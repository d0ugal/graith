//go:build darwin

package client

import "golang.org/x/sys/unix"

func daemonPIDFromFD(fd int) (int, error) {
	return unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
}
