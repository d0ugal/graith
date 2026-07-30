package protocol

import (
	"testing"
	"time"
)

func TestUpgradeNegotiationTimeoutCoversPreAckPhases(t *testing.T) {
	phases := map[string]time.Duration{
		"target version probe":      5 * time.Second,
		"target capacity probe":     5 * time.Second,
		"admission drain":           15 * time.Second,
		"background drain":          10 * time.Second,
		"terminal helper handoff":   5 * time.Second,
		"handoff preparation slack": 5 * time.Second,
	}

	var total time.Duration
	for _, duration := range phases {
		total += duration
	}

	if UpgradeNegotiationTimeout < total {
		t.Fatalf("UpgradeNegotiationTimeout = %v, want at least %v", UpgradeNegotiationTimeout, total)
	}
}
