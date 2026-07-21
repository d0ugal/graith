//go:build !linux && !darwin

package daemon

import "context"

func openFDCounts(_ context.Context, _ []int) map[int]int { return nil }
