//go:build integration && releaseartifact && (linux || (darwin && arm64 && cgo))

package integration

import (
	"errors"
	"fmt"
)

var errNativeProcessObservationChurn = errors.New("native process observation churn")

func nativeProcessObservationChurn(message string) error {
	return fmt.Errorf("%s: %w", message, errNativeProcessObservationChurn)
}
