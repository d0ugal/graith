//go:build !linux && !darwin

package daemon

func openFDCounts(_ []int) map[int]int { return nil }
