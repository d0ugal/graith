//go:build darwin

package daemonservice

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func currentMacOSMajor() (int, error) {
	return currentMacOSMajorContext(context.Background())
}

func currentMacOSMajorContext(parent context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion").Output()
	if err != nil {
		return 0, fmt.Errorf("read macOS version: %w", err)
	}

	majorText, _, _ := strings.Cut(strings.TrimSpace(string(output)), ".")

	major, err := strconv.Atoi(majorText)
	if err != nil || major <= 0 {
		return 0, fmt.Errorf("parse macOS version %q", strings.TrimSpace(string(output)))
	}

	return major, nil
}
