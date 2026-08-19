package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/d0ugal/graith/internal/dependencyhealth"
)

func TestDependencyHealthStateRoundTripAndOlderVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := NewState()
	state.DependencyHealth.Services["github"] = dependencyhealth.ServiceState{ObservedState: dependencyhealth.Degraded, SourceHealth: dependencyhealth.Fresh, Generation: 2}

	state.DependencyHealth.Outbox = []dependencyhealth.Transition{{Service: "github", Generation: 2, ObservedState: dependencyhealth.Degraded, TargetSessionID: "braw"}}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := loaded.DependencyHealth.Services["github"].ObservedState; got != dependencyhealth.Degraded {
		t.Fatalf("state = %q", got)
	}

	data, err := json.Marshal(map[string]any{"version": CurrentStateVersion - 1, "sessions": map[string]any{}, "dependency_health": state.DependencyHealth})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err = LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.DependencyHealth == nil || loaded.DependencyHealth.Services["github"].Generation != 2 {
		t.Fatal("dependency health lost during migration")
	}
}

func TestDependencyHealthCorruptionDoesNotDiscardSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	data := []byte(`{"version":` + strconv.Itoa(CurrentStateVersion) + `,"sessions":{"braw":{"id":"braw","name":"braw","status":"running"}},"dependency_health":{"schema_version":1,"services":"dreich"}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if state.Sessions["braw"] == nil || state.DependencyHealth == nil || len(state.DependencyHealth.Services) != 0 {
		t.Fatalf("state = %+v", state)
	}
}

func TestDependencyHealthSidecarSurvivesOlderDaemonRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := NewState()

	state.DependencyHealth.Services["github"] = dependencyhealth.ServiceState{ObservedState: dependencyhealth.Down, SourceHealth: dependencyhealth.Fresh}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	// Simulate an older daemon rewriting the main envelope without the optional
	// field. The sidecar is outside that daemon's schema and must survive.
	legacy := []byte(`{"version":32,"sessions":{}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.DependencyHealth.Services["github"].ObservedState != dependencyhealth.Down {
		t.Fatal("sidecar health was lost")
	}
}
