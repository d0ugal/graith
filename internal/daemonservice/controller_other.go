//go:build !darwin

package daemonservice

import (
	"context"
	"errors"
)

type DarwinController struct{}

var errServiceUnavailable = errors.New("macOS Service Management is unavailable on this platform")

func (DarwinController) Status(context.Context, string, Definition) (ServiceStatus, error) {
	return "", errServiceUnavailable
}
func (DarwinController) Register(context.Context, string, Definition) (ServiceStatus, error) {
	return "", errServiceUnavailable
}
func (DarwinController) RegisterFresh(context.Context, string, Definition) (ServiceStatus, error) {
	return "", errServiceUnavailable
}
func (DarwinController) Unregister(context.Context, string, Definition) (ServiceStatus, error) {
	return "", errServiceUnavailable
}
func (DarwinController) Kickstart(context.Context, int, Definition) error {
	return errServiceUnavailable
}
func (DarwinController) JobState(context.Context, int, Definition) (JobState, error) {
	return JobState{}, errServiceUnavailable
}
