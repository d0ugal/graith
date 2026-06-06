package client

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func EnsureDaemon(sockPath string) (net.Conn, error) {
	if conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond); err == nil {
		return conn, nil
	}

	if err := startDaemon(); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		if conn, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond); err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("daemon did not start in time")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func startDaemon() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(self, "daemon", "start")
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	return cmd.Start()
}
