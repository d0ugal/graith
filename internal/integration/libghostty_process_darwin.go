//go:build integration && releaseartifact && cgo && darwin && arm64

package integration

/*
#include <errno.h>
#include <libproc.h>
#include <stdint.h>
#include <stdlib.h>
#include <sys/proc_info.h>

static int graith_child_count(int parent, int *count, int *error_number) {
    errno = 0;
    *count = proc_listchildpids(parent, NULL, 0);
    *error_number = errno;
    return *count < 0 || (*count == 0 && *error_number != 0) ? -1 : 0;
}

static int graith_list_children(
    int parent,
    int *pids,
    int count,
    int *listed,
    int *error_number
) {
    errno = 0;
    *listed = proc_listchildpids(parent, pids, count * (int)sizeof(int));
    *error_number = errno;
    return *listed < 0 || (*listed == 0 && *error_number != 0) ? -1 : 0;
}

static int graith_pid_path(int pid, char *buffer, int size) {
    return proc_pidpath(pid, buffer, size);
}

static int graith_fd_count(int pid) {
    int bytes = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
    if (bytes <= 0) return -1;
    struct proc_fdinfo *fds = malloc((size_t)bytes);
    if (fds == NULL) return -1;
    int actual = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, fds, bytes);
    free(fds);
    if (actual <= 0 || actual % (int)sizeof(struct proc_fdinfo) != 0) return -1;
    return actual / (int)sizeof(struct proc_fdinfo);
}

static int64_t graith_resident_size(int pid) {
    struct proc_taskinfo info;
    int bytes = proc_pidinfo(pid, PROC_PIDTASKINFO, 0, &info, sizeof(info));
    if (bytes != sizeof(info)) return -1;
    return (int64_t)info.pti_resident_size;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"syscall"
	"unsafe"

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

// nativeChildCapacity returns the buffer size recommended by
// proc_listchildpids. Darwin does not guarantee that the sizing call is the
// current child count; only the populated call returns that count.
func nativeChildCapacity(parent int, operation string) (int, error) {
	var capacity C.int
	var errorNumber C.int

	status := C.graith_child_count(C.int(parent), &capacity, &errorNumber)
	if status != 0 {
		if errorNumber != 0 {
			return 0, fmt.Errorf("%s: %w", operation, syscall.Errno(errorNumber))
		}

		return 0, errors.New(operation)
	}

	return int(capacity), nil
}

func nativeListChildren(parent int, children []C.int) (int, error) {
	var listed C.int
	var errorNumber C.int

	status := C.graith_list_children(
		C.int(parent),
		(*C.int)(unsafe.Pointer(&children[0])),
		C.int(len(children)),
		&listed,
		&errorNumber,
	)
	if status != 0 {
		if errorNumber != 0 {
			return 0, fmt.Errorf("read daemon child processes: %w", syscall.Errno(errorNumber))
		}

		return 0, errors.New("read daemon child processes")
	}

	return int(listed), nil
}

func nativeChildPIDs(parent nativeProcessIdentity) ([]int, error) {
	current, err := nativeProcessIsCurrent(parent)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, errors.New("daemon process identity is no longer current")
	}

	const attempts = 4
	minimumCapacity := 0
	for range attempts {
		capacity, capacityErr := nativeChildCapacity(parent.PID, "size daemon child process buffer")
		if capacityErr != nil {
			return nil, capacityErr
		}
		capacity = max(capacity, minimumCapacity)
		if capacity == 0 {
			current, currentErr := nativeProcessIsCurrent(parent)
			if currentErr != nil {
				return nil, currentErr
			}
			if !current {
				return nil, errors.New("daemon process identity changed during child inspection")
			}

			resized, resizeErr := nativeChildCapacity(parent.PID, "resize daemon child process buffer")
			if resizeErr != nil {
				return nil, resizeErr
			}
			if resized != 0 {
				continue
			}

			return nil, nil
		}

		children := make([]C.int, capacity)
		got, listErr := nativeListChildren(parent.PID, children)
		if listErr != nil {
			return nil, listErr
		}
		if got > len(children) {
			return nil, errors.New("daemon child process count exceeds its buffer")
		}
		if got == len(children) {
			minimumCapacity = len(children) * 2
			continue
		}

		result := make([]int, got)
		seen := make(map[int]struct{}, got)
		for i, child := range children[:got] {
			pid := int(child)
			if pid <= 1 {
				return nil, errors.New("daemon child process ID is invalid")
			}
			if _, duplicate := seen[pid]; duplicate {
				return nil, errors.New("daemon child process list contains duplicates")
			}
			seen[pid] = struct{}{}
			result[i] = pid
		}

		current, currentErr := nativeProcessIsCurrent(parent)
		if currentErr != nil {
			return nil, currentErr
		}
		if !current {
			return nil, errors.New("daemon process identity changed during child inspection")
		}

		return result, nil
	}

	return nil, errors.New("daemon child process list did not stabilize")
}

func nativeHelperChildProcesses(parent nativeProcessIdentity) ([]nativeProcessIdentity, error) {
	// Darwin helpers execute from the daemon-pinned private image, whose
	// stable basename is deliberately distinct from the public gr binary.
	const wantExecutable = "graith-helper"
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
			buffer := make([]byte, C.PROC_PIDPATHINFO_MAXSIZE)
			length := int(C.graith_pid_path(
				C.int(child), (*C.char)(unsafe.Pointer(&buffer[0])), C.int(len(buffer)),
			))
			if length <= 0 || length > len(buffer) {
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
			if filepath.Base(string(buffer[:length])) == wantExecutable {
				helpers = append(helpers, identity)
			}
		}
		if churned {
			continue
		}

		current, currentErr := nativeProcessIsCurrent(parent)
		if currentErr != nil {
			return nil, currentErr
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

	count := int(C.graith_fd_count(C.int(identity.PID)))
	if count <= 0 {
		return 0, errors.New("read process file descriptors")
	}

	current, err = nativeProcessIsCurrent(identity)
	if err != nil {
		return 0, err
	}
	if !current {
		return 0, errors.New("daemon process identity changed during file descriptor inspection")
	}

	return count, nil
}

func nativeDaemonRSSBytes(identity nativeProcessIdentity) (int64, error) {
	current, err := nativeProcessIsCurrent(identity)
	if err != nil {
		return 0, err
	}
	if !current {
		return 0, errors.New("daemon process identity is no longer current")
	}

	bytes := int64(C.graith_resident_size(C.int(identity.PID)))
	if bytes <= 0 {
		return 0, errors.New("read process resident memory")
	}

	current, err = nativeProcessIsCurrent(identity)
	if err != nil {
		return 0, err
	}
	if !current {
		return 0, errors.New("daemon process identity changed during resident memory inspection")
	}

	return bytes, nil
}
