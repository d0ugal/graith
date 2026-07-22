//go:build integration && libghostty && cgo && linux

package integration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	grpty "github.com/d0ugal/graith/internal/pty"
)

type nativeProcessIdentity struct {
	PID       int
	StartTime int64
}

func nativeProcessIdentityMatchesStart(
	identity nativeProcessIdentity,
	startTime int64,
	observationErr error,
) (bool, error) {
	if identity.PID <= 1 || identity.StartTime <= 0 {
		return false, errors.New("native process identity is invalid")
	}
	if observationErr != nil {
		return false, observationErr
	}
	if startTime <= 0 {
		return false, errors.New("observed native process start time is invalid")
	}

	return startTime == identity.StartTime, nil
}

func nativeProcessExistenceFromSignal(signalErr error) (bool, error) {
	if signalErr == nil {
		return true, nil
	}
	if errors.Is(signalErr, syscall.ESRCH) {
		return false, nil
	}

	return false, signalErr
}

func nativeProcessExists(pid int) (bool, error) {
	if pid <= 1 {
		return false, errors.New("native process ID is invalid")
	}

	exists, err := nativeProcessExistenceFromSignal(syscall.Kill(pid, 0))
	if err != nil {
		return false, fmt.Errorf("confirm native process existence: %w", err)
	}

	return exists, nil
}

func nativeProcessStartTime(pid int) (int64, bool, error) {
	if pid <= 1 {
		return 0, false, errors.New("native process ID is invalid")
	}

	startTime, observationErr := grpty.ProcessStartTime(pid)
	if observationErr == nil {
		if startTime <= 0 {
			return 0, true, errors.New("observed native process start time is invalid")
		}

		return startTime, true, nil
	}

	exists, existenceErr := nativeProcessExists(pid)
	if existenceErr != nil {
		return 0, false, errors.Join(
			fmt.Errorf("observe native process start time: %w", observationErr),
			existenceErr,
		)
	}
	if !exists {
		return 0, false, nil
	}

	return 0, true, fmt.Errorf("observe native process start time: %w", observationErr)
}

func nativeProcessIsCurrent(identity nativeProcessIdentity) (bool, error) {
	startTime, exists, err := nativeProcessStartTime(identity.PID)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}

	return nativeProcessIdentityMatchesStart(identity, startTime, nil)
}

func nativeChildPIDs(parent nativeProcessIdentity) ([]int, error) {
	const attempts = 4
	for range attempts {
		current, err := nativeProcessIsCurrent(parent)
		if err != nil {
			return nil, err
		}
		if !current {
			return nil, errors.New("daemon process identity is no longer current")
		}

		taskRoot := fmt.Sprintf("/proc/%d/task", parent.PID)
		tasks, err := os.ReadDir(taskRoot)
		if err != nil {
			return nil, fmt.Errorf("read daemon tasks: %w", err)
		}

		children := make([]int, 0)
		seen := make(map[int]struct{})
		valid := true
		for _, task := range tasks {
			if !task.IsDir() {
				continue
			}
			if _, parseErr := strconv.Atoi(task.Name()); parseErr != nil {
				return nil, errors.New("daemon task directory contains an invalid thread ID")
			}
			path := filepath.Join(taskRoot, task.Name(), "children")
			data, readErr := os.ReadFile(path)
			if errors.Is(readErr, os.ErrNotExist) {
				valid = false
				break
			}
			if readErr != nil {
				return nil, fmt.Errorf("read daemon child processes: %w", readErr)
			}
			for _, field := range strings.Fields(string(data)) {
				pid, parseErr := strconv.Atoi(field)
				if parseErr != nil || pid <= 1 {
					return nil, errors.New("daemon child process list contains an invalid process ID")
				}
				if _, duplicate := seen[pid]; duplicate {
					continue
				}
				seen[pid] = struct{}{}

				if _, exists, observeErr := nativeProcessStartTime(pid); observeErr != nil {
					return nil, observeErr
				} else if !exists {
					valid = false
					break
				}
				children = append(children, pid)
			}
			if !valid {
				break
			}
		}
		if !valid {
			continue
		}

		current, err = nativeProcessIsCurrent(parent)
		if err != nil {
			return nil, err
		}
		if !current {
			return nil, errors.New("daemon process identity changed during child inspection")
		}

		sort.Ints(children)

		return children, nil
	}

	return nil, errors.New("daemon child process list did not stabilize")
}

func nativeHelperChildProcesses(parent nativeProcessIdentity) ([]nativeProcessIdentity, error) {
	const attempts = 4
	for range attempts {
		children, err := nativeChildPIDs(parent)
		if err != nil {
			return nil, err
		}

		helpers := make([]nativeProcessIdentity, 0, len(children))
		churned := false
		for _, child := range children {
			startTime, exists, startErr := nativeProcessStartTime(child)
			if startErr != nil {
				return nil, startErr
			}
			if !exists {
				churned = true
				break
			}
			identity := nativeProcessIdentity{PID: child, StartTime: startTime}

			target, readErr := os.Readlink(fmt.Sprintf("/proc/%d/exe", child))
			if readErr != nil {
				current, currentErr := nativeProcessIsCurrent(identity)
				if currentErr != nil {
					return nil, currentErr
				}
				if current {
					return nil, errors.New("read executable path for live daemon child")
				}

				churned = true
				break
			}

			current, currentErr := nativeProcessIsCurrent(identity)
			if currentErr != nil {
				return nil, currentErr
			}
			if !current {
				churned = true
				break
			}
			if strings.HasPrefix(filepath.Base(target), "memfd:graith-helper-image") {
				helpers = append(helpers, identity)
			}
		}
		if churned {
			continue
		}

		current, err := nativeProcessIsCurrent(parent)
		if err != nil {
			return nil, err
		}
		if !current {
			return nil, errors.New("daemon process identity changed during helper inspection")
		}

		sort.Slice(helpers, func(i, j int) bool {
			if helpers[i].PID == helpers[j].PID {
				return helpers[i].StartTime < helpers[j].StartTime
			}

			return helpers[i].PID < helpers[j].PID
		})

		return helpers, nil
	}

	return nil, errors.New("daemon helper process list did not stabilize")
}

func nativeDaemonFDCount(identity nativeProcessIdentity) (int, error) {
	current, err := nativeProcessIsCurrent(identity)
	if err != nil {
		return 0, err
	}
	if !current {
		return 0, errors.New("daemon process identity is no longer current")
	}

	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", identity.PID))
	if err != nil || len(entries) == 0 {
		if err != nil {
			return 0, fmt.Errorf("read process file descriptors: %w", err)
		}

		return 0, errors.New("read process file descriptors")
	}

	current, err = nativeProcessIsCurrent(identity)
	if err != nil {
		return 0, err
	}
	if !current {
		return 0, errors.New("daemon process identity changed during file descriptor inspection")
	}

	return len(entries), nil
}

func nativeDaemonRSSBytes(identity nativeProcessIdentity) (int64, error) {
	current, err := nativeProcessIsCurrent(identity)
	if err != nil {
		return 0, err
	}
	if !current {
		return 0, errors.New("daemon process identity is no longer current")
	}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", identity.PID))
	if err != nil {
		return 0, fmt.Errorf("read process resident memory: %w", err)
	}
	var rssKiB int64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "VmRSS:" || fields[2] != "kB" {
			continue
		}
		rssKiB, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil || rssKiB <= 0 {
			return 0, errors.New("process resident memory record is invalid")
		}
		break
	}
	if rssKiB <= 0 {
		return 0, errors.New("process resident memory record is missing")
	}

	current, err = nativeProcessIsCurrent(identity)
	if err != nil {
		return 0, err
	}
	if !current {
		return 0, errors.New("daemon process identity changed during resident memory inspection")
	}

	return rssKiB * 1024, nil
}
