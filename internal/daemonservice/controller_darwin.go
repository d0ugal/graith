//go:build darwin

package daemonservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/testprocess"
)

type DarwinController struct{}

const serviceOperationTimeout = 10 * time.Second

type limitedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *limitedBuffer) Write(data []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 || len(data) > remaining {
		return 0, errors.New("command output exceeds limit")
	}

	return w.buffer.Write(data)
}

type controllerResponse struct {
	Operation string        `json:"operation"`
	Service   string        `json:"service"`
	Status    ServiceStatus `json:"status"`
}

func (DarwinController) invoke(ctx context.Context, controllerPath string, definition Definition, operation string) (ServiceStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, controllerPath, definition.Slot, operation)
	cmd.Env = []string{}

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &limitedBuffer{buffer: &stdout, limit: 4096}

	cmd.Stderr = &limitedBuffer{buffer: &stderr, limit: 4096}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("service controller %s: %w (%s)", operation, err, strings.TrimSpace(stderr.String()))
	}

	var response controllerResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return "", fmt.Errorf("decode service controller response: %w", err)
	}

	if response.Operation != operation || response.Service != definition.Slot {
		return "", fmt.Errorf(
			"service controller response mismatch: operation=%q service=%q, want operation=%q service=%q",
			response.Operation, response.Service, operation, definition.Slot,
		)
	}

	if err := validateControllerStatus(response.Status); err != nil {
		return "", err
	}

	return response.Status, nil
}

func (controller DarwinController) Status(ctx context.Context, path string, definition Definition) (ServiceStatus, error) {
	return controller.invoke(ctx, path, definition, "status")
}

func (controller DarwinController) Register(ctx context.Context, path string, definition Definition) (ServiceStatus, error) {
	if err := testprocess.RefuseDaemonLifecycleMutation("register managed daemon service"); err != nil {
		return "", err
	}

	return controller.invoke(ctx, path, definition, "register")
}

func (controller DarwinController) RegisterFresh(ctx context.Context, path string, definition Definition) (ServiceStatus, error) {
	if err := testprocess.RefuseDaemonLifecycleMutation("register fresh managed daemon service"); err != nil {
		return "", err
	}

	return controller.invoke(ctx, path, definition, "register-fresh")
}

func (controller DarwinController) Unregister(ctx context.Context, path string, definition Definition) (ServiceStatus, error) {
	if err := testprocess.RefuseDaemonLifecycleMutation("unregister managed daemon service"); err != nil {
		return "", err
	}

	return controller.invoke(ctx, path, definition, "unregister")
}

func (DarwinController) Kickstart(ctx context.Context, uid int, definition Definition) error {
	if err := testprocess.RefuseDaemonLifecycleMutation("kickstart managed daemon service"); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()

	target := fmt.Sprintf("gui/%d/%s", uid, definition.Label)

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, "/bin/launchctl", "kickstart", "-kp", target)
	cmd.Env = []string{}
	cmd.Stdout = &limitedBuffer{buffer: &stdout, limit: 4096}
	cmd.Stderr = &limitedBuffer{buffer: &stderr, limit: 4096}

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("kickstart %s: %w (%s)", target, err, strings.TrimSpace(stderr.String()+stdout.String()))
	}

	return nil
}

func (DarwinController) JobState(ctx context.Context, uid int, definition Definition) (JobState, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()

	target := fmt.Sprintf("gui/%d/%s", uid, definition.Label)

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, "/bin/launchctl", "print", target)
	cmd.Env = []string{}
	cmd.Stdout = &limitedBuffer{buffer: &stdout, limit: 4096}
	cmd.Stderr = &limitedBuffer{buffer: &stderr, limit: 4096}
	err := cmd.Run()
	output := stdout.String() + stderr.String()

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && launchctlReportsMissing(output) {
			return JobState{}, nil
		}

		return JobState{}, fmt.Errorf("inspect launchd job %s: %w (%s)", target, err, strings.TrimSpace(output))
	}

	return parseLaunchctlJobState(output), nil
}

func parseLaunchctlJobState(output string) JobState {
	state := JobState{Present: true}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "state = running" {
			state.Running = true
		}

		if strings.HasPrefix(line, "pid = ") {
			state.PID, _ = strconv.Atoi(strings.TrimPrefix(line, "pid = "))
		}

		if strings.HasPrefix(line, "program = ") {
			state.Program = strings.Trim(strings.TrimPrefix(line, "program = "), `"`)
		}

		if strings.HasPrefix(line, "program identifier = ") {
			identifier := strings.TrimPrefix(line, "program identifier = ")
			state.ProgramIdentifier, _, _ = strings.Cut(identifier, " ")
		}

		if strings.HasPrefix(line, "parent bundle identifier = ") {
			state.ParentBundleIdentifier = strings.Trim(strings.TrimPrefix(line, "parent bundle identifier = "), `"`)
		}

		if strings.HasPrefix(line, "parent bundle version = ") {
			state.ParentBundleVersion = strings.Trim(strings.TrimPrefix(line, "parent bundle version = "), `"`)
		}
	}

	return state
}

func launchctlReportsMissing(output string) bool {
	output = strings.ToLower(output)
	return strings.Contains(output, "could not find service") || strings.Contains(output, "service not found")
}
