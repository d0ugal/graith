package daemonservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ServiceStatus string

const (
	StatusNotRegistered    ServiceStatus = "not-registered"
	StatusEnabled          ServiceStatus = "enabled"
	StatusRequiresApproval ServiceStatus = "requires-approval"
	StatusNotFound         ServiceStatus = "not-found"
)

type JobState struct {
	Present                bool
	Running                bool
	PID                    int
	Program                string
	ProgramIdentifier      string
	ParentBundleIdentifier string
	ParentBundleVersion    string
}

type ServiceController interface {
	Status(ctx context.Context, controllerPath string, definition Definition) (ServiceStatus, error)
	Register(ctx context.Context, controllerPath string, definition Definition) (ServiceStatus, error)
	// RegisterFresh registers only when the controller can prove the job was
	// absent immediately before this call. It must return an error rather than
	// normalize an already-registered race to StatusEnabled.
	RegisterFresh(ctx context.Context, controllerPath string, definition Definition) (ServiceStatus, error)
	Unregister(ctx context.Context, controllerPath string, definition Definition) (ServiceStatus, error)
	Kickstart(ctx context.Context, uid int, definition Definition) error
	JobState(ctx context.Context, uid int, definition Definition) (JobState, error)
}

func registerFreshService(ctx context.Context, controller ServiceController, controllerPath string, definition Definition) error {
	status, err := controller.Status(ctx, controllerPath, definition)
	if err != nil {
		return err
	}

	if err := validateControllerStatus(status); err != nil {
		return err
	}

	if status != StatusNotRegistered && status != StatusNotFound {
		return fmt.Errorf("fresh Graith background service registration requires an absent job, found status %q", status)
	}

	status, err = controller.RegisterFresh(ctx, controllerPath, definition)
	if err != nil {
		return fmt.Errorf("register fresh Graith background service: %w", err)
	}

	if status == StatusRequiresApproval {
		return errors.New("graith background service requires approval in System Settings > General > Login Items")
	}

	if status != StatusEnabled {
		return fmt.Errorf("fresh Graith background service registration ended in status %q", status)
	}

	return nil
}

func validateControllerStatus(status ServiceStatus) error {
	switch status {
	case StatusNotRegistered, StatusEnabled, StatusRequiresApproval, StatusNotFound:
		return nil
	default:
		return fmt.Errorf("unknown Service Management status %q", status)
	}
}

func registerService(ctx context.Context, controller ServiceController, controllerPath string, definition Definition) error {
	status, err := controller.Status(ctx, controllerPath, definition)
	if err != nil {
		return err
	}

	if err := validateControllerStatus(status); err != nil {
		return err
	}

	switch status {
	case StatusEnabled:
		return nil
	case StatusRequiresApproval:
		return errors.New("graith background service requires approval in System Settings > General > Login Items")
	case StatusNotRegistered, StatusNotFound:
		status, err = controller.Register(ctx, controllerPath, definition)
		if err != nil {
			return fmt.Errorf("register Graith background service: %w", err)
		}

		if status == StatusRequiresApproval {
			return errors.New("graith background service requires approval in System Settings > General > Login Items")
		}

		if status != StatusEnabled {
			return fmt.Errorf("graith background service registration ended in status %q", status)
		}

		return nil
	default:
		return fmt.Errorf("unsupported Service Management status %q", status)
	}
}

func waitForJobAbsent(ctx context.Context, controller ServiceController, uid int, definition Definition) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		state, err := controller.JobState(ctx, uid, definition)
		if err == nil && !state.Present {
			return nil
		}

		select {
		case <-ctx.Done():
			if err != nil {
				return errors.Join(ctx.Err(), err)
			}

			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func controllerExecutable(appPath string) string {
	return strings.TrimSuffix(appPath, string('/')) + "/Contents/MacOS/" + ControllerExecutable
}
