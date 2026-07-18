//go:build linux

package client

import "golang.org/x/sys/unix"

func daemonPIDFromFD(fd int) (int, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}

	return int(cred.Pid), nil
}
