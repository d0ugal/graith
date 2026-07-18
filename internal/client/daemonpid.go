package client

import (
	"errors"
	"fmt"
	"net"
	"syscall"
)

func daemonPID(conn net.Conn) (int, error) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return 0, errors.New("daemon connection does not expose a socket descriptor")
	}

	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("access daemon socket: %w", err)
	}

	var (
		pid     int
		peerErr error
	)

	if err := raw.Control(func(fd uintptr) {
		pid, peerErr = daemonPIDFromFD(int(fd))
	}); err != nil {
		return 0, fmt.Errorf("inspect daemon socket: %w", err)
	}

	if peerErr != nil {
		return 0, fmt.Errorf("read daemon peer PID: %w", peerErr)
	}

	if pid <= 1 {
		return 0, fmt.Errorf("invalid daemon peer PID %d", pid)
	}

	return pid, nil
}
