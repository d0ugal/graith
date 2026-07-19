//go:build !darwin

package daemonservice

import (
	"context"
	"errors"
)

func ServiceControlRoot(int) (string, error) {
	return "", errors.New("macOS daemon service is unavailable on this platform")
}

func ServiceControlRootContext(context.Context, int) (string, error) {
	return "", errors.New("macOS daemon service is unavailable on this platform")
}
