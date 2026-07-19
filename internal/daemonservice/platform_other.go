//go:build !darwin

package daemonservice

import "context"

func currentMacOSMajor() (int, error) { return 0, nil }

func currentMacOSMajorContext(context.Context) (int, error) { return 0, nil }
