//go:build darwin

package daemonservice

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// swVersTimeout bounds one sw_vers run. A loaded machine (for example a busy CI
// runner) can take longer than a second to start the process, which killed it
// and failed daemon startup.
const swVersTimeout = 10 * time.Second

// readMacOSVersion runs sw_vers. It is a variable so tests can substitute it.
var readMacOSVersion = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion").Output()
}

// macOSMajorCache holds the first successful result: the OS version cannot
// change while the process runs, so later callers skip the subprocess.
var macOSMajorCache struct {
	sync.Mutex

	major int
}

func currentMacOSMajor() (int, error) {
	return currentMacOSMajorContext(context.Background())
}

func currentMacOSMajorContext(parent context.Context) (int, error) {
	macOSMajorCache.Lock()
	defer macOSMajorCache.Unlock()

	if macOSMajorCache.major > 0 {
		return macOSMajorCache.major, nil
	}

	ctx, cancel := context.WithTimeout(parent, swVersTimeout)
	defer cancel()

	output, err := readMacOSVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("read macOS version: %w", err)
	}

	majorText, _, _ := strings.Cut(strings.TrimSpace(string(output)), ".")

	major, err := strconv.Atoi(majorText)
	if err != nil || major <= 0 {
		return 0, fmt.Errorf("parse macOS version %q", strings.TrimSpace(string(output)))
	}

	macOSMajorCache.major = major

	return major, nil
}
