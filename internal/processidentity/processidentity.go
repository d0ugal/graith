// Package processidentity identifies graith-owned operating-system processes.
package processidentity

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/d0ugal/graith/internal/tools"
)

type commandOutput func(name string, args ...string) ([]byte, error)

// IsGraithDaemon reports whether pid is a live process whose executable name
// exactly matches one of the graith daemon binaries. Any inability to verify
// the process identity is treated as a mismatch.
func IsGraithDaemon(pid int) bool {
	return isGraithDaemon(pid, isProcessAlive, runCommand)
}

func isGraithDaemon(pid int, alive func(int) bool, output commandOutput) bool {
	if pid <= 1 || !alive(pid) {
		return false
	}

	out, err := processCommand(pid, output)
	if err != nil {
		return false
	}

	base := filepath.Base(strings.TrimSpace(string(out)))

	return base == "gr" || base == "graith"
}

func processCommand(pid int, output commandOutput) ([]byte, error) {
	return output(tools.PS(), "-p", strconv.Itoa(pid), "-o", "comm=")
}

func runCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))

	return err == nil || errors.Is(err, syscall.EPERM)
}
