package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func TestStateSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	state := &State{
		Sessions: map[string]*SessionState{
			"braw123": {
				ID: "braw123", Name: "bonnie-fix", RepoPath: "/hame/glen/croft",
				RepoName: "croft", WorktreePath: "/hame/glen/.local/share/graith/worktrees/braw123",
				Branch: "d0ugal/graith/bonnie-fix-braw123", BaseBranch: "main",
				Agent: "claude", Status: StatusRunning, CreatedAt: time.Now().UTC(),
			},
		},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d", loaded.Version, CurrentStateVersion)
	}

	s, ok := loaded.Sessions["braw123"]
	if !ok {
		t.Fatal("session not found after load")
	}

	if s.Name != "bonnie-fix" || s.Agent != "claude" || s.Status != StatusRunning {
		t.Errorf("session = %+v", s)
	}
}

func TestLoadStateV0Migration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	// Write a v0 state file (no version field)
	v0Data := []byte(`{"sessions":{"braw1":{"id":"braw1","name":"auld-kirk","status":"running"}}}`)
	if err := writeFileAtomic(path, v0Data); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if state.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d after migration", state.Version, CurrentStateVersion)
	}

	if s, ok := state.Sessions["braw1"]; !ok {
		t.Fatal("session lost during migration")
	} else if s.Name != "auld-kirk" {
		t.Errorf("name = %q, want %q", s.Name, "auld-kirk")
	}
}

func TestLoadStateFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	futureData := []byte(`{"version":999,"sessions":{}}`)
	if err := writeFileAtomic(path, futureData); err != nil {
		t.Fatal(err)
	}

	_, err := LoadState(path)
	if err == nil {
		t.Fatal("expected error for future version")
	}
}

func TestSaveStateSetsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	state := &State{Version: 0, Sessions: make(map[string]*SessionState)}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}

	if state.Version != CurrentStateVersion {
		t.Errorf("version = %d after save, want %d", state.Version, CurrentStateVersion)
	}
}

func TestLoadStateMissing(t *testing.T) {
	state, err := LoadState("/nonexistent/state.json")
	if err != nil {
		t.Fatal(err)
	}

	if len(state.Sessions) != 0 {
		t.Error("expected empty state for missing file")
	}
}

func TestLoadStateCorrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeFileAtomic(path, []byte("not json")); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(state.Sessions) != 0 {
		t.Error("expected empty state for corrupted file")
	}
}

func TestWriteFileAtomicContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.json")

	want := []byte(`{"hello":"world"}`)
	if err := writeFileAtomic(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != string(want) {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestWriteFileAtomicPreservesOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	original := []byte(`{"original":true}`)
	if err := writeFileAtomic(path, original); err != nil {
		t.Fatal(err)
	}

	// Writing to a read-only directory should fail, leaving the original intact.
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: 0500 read-only dir is the point of this write-failure test
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(dir, 0o750) }) //nolint:gosec // G302: restoring dir perms; a dir needs the execute bit to be traversable

	err := writeFileAtomic(path, []byte(`{"new":true}`))
	if err == nil {
		t.Fatal("expected error writing to read-only dir")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != string(original) {
		t.Errorf("original file was corrupted: got %q", got)
	}
}

func TestWriteFileAtomicNoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "test.json")
	if err := writeFileAtomic(path, []byte("data")); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if e.Name() != "test.json" {
			t.Errorf("unexpected temp file left behind: %s", e.Name())
		}
	}
}

func TestSandboxConfigPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	sbx := &config.SandboxConfig{
		Enabled:   true,
		Command:   "safehouse",
		Features:  []string{"net"},
		ReadDirs:  []string{"/usr/share"},
		WriteDirs: []string{"/tmp"},
	}

	state := &State{
		Sessions: map[string]*SessionState{
			"braw1": {
				ID: "braw1", Name: "kirk-sandbox", Agent: "claude",
				Status: StatusRunning, Sandboxed: true,
				SandboxConfig: sbx,
				CreatedAt:     time.Now().UTC(),
			},
		},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	s := loaded.Sessions["braw1"]
	if s.SandboxConfig == nil {
		t.Fatal("SandboxConfig lost after save/load")
	}

	if !s.SandboxConfig.Enabled {
		t.Error("SandboxConfig.Enabled = false, want true")
	}

	if s.SandboxConfig.Command != "safehouse" {
		t.Errorf("SandboxConfig.Command = %q, want %q", s.SandboxConfig.Command, "safehouse")
	}

	if len(s.SandboxConfig.ReadDirs) != 1 || s.SandboxConfig.ReadDirs[0] != "/usr/share" {
		t.Errorf("SandboxConfig.ReadDirs = %v, want [/usr/share]", s.SandboxConfig.ReadDirs)
	}

	if len(s.SandboxConfig.WriteDirs) != 1 || s.SandboxConfig.WriteDirs[0] != "/tmp" {
		t.Errorf("SandboxConfig.WriteDirs = %v, want [/tmp]", s.SandboxConfig.WriteDirs)
	}

	if len(s.SandboxConfig.Features) != 1 || s.SandboxConfig.Features[0] != "net" {
		t.Errorf("SandboxConfig.Features = %v, want [net]", s.SandboxConfig.Features)
	}
}

func TestMigrateV12ToV13PairedDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// A v12 state with no paired_devices field, as written by an older binary.
	data := []byte(`{"version":12,"sessions":{
		"braw1":{"id":"braw1","name":"bide-session","status":"running"}
	}}`)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d after migration", loaded.Version, CurrentStateVersion)
	}

	if loaded.PairedDevices == nil {
		t.Fatal("PairedDevices is nil after migration; want an initialized map")
	}

	if len(loaded.PairedDevices) != 0 {
		t.Errorf("PairedDevices has %d entries, want 0", len(loaded.PairedDevices))
	}

	// The HMAC key is generated lazily, not by the migration.
	if loaded.PairingHMACKey != "" {
		t.Errorf("PairingHMACKey = %q, want empty until first pairing", loaded.PairingHMACKey)
	}

	// The pre-existing session must survive the migration untouched.
	if s := loaded.Sessions["braw1"]; s == nil || s.Name != "bide-session" {
		t.Error("braw1 session lost or altered during v12→v13 migration")
	}
}

func TestEnsurePairingHMACKey(t *testing.T) {
	s := NewState()

	if s.PairingHMACKey != "" {
		t.Fatalf("fresh state PairingHMACKey = %q, want empty", s.PairingHMACKey)
	}

	k1, err := s.EnsurePairingHMACKey()
	if err != nil {
		t.Fatal(err)
	}

	if k1 == "" {
		t.Fatal("EnsurePairingHMACKey returned empty key")
	}

	// Idempotent: a second call returns the same stored key.
	k2, err := s.EnsurePairingHMACKey()
	if err != nil {
		t.Fatal(err)
	}

	if k2 != k1 {
		t.Errorf("second EnsurePairingHMACKey = %q, want stable %q", k2, k1)
	}

	if s.PairingHMACKey != k1 {
		t.Errorf("PairingHMACKey = %q, want %q", s.PairingHMACKey, k1)
	}
}

func TestMigrateApprovalsEnabledToAgentHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	data := []byte(`{"version":3,"sessions":{
		"braw1":{"id":"braw1","name":"braw-approvals","status":"running","approvals_enabled":true},
		"canny1":{"id":"canny1","name":"neep-approvals","status":"running"},
		"kirk1":{"id":"kirk1","name":"auld-migrated","status":"running","agent_hooks":true}
	}}`)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d", loaded.Version, CurrentStateVersion)
	}

	if s := loaded.Sessions["braw1"]; !s.AgentHooks {
		t.Error("braw1: AgentHooks = false, want true (migrated from approvals_enabled)")
	}

	if s := loaded.Sessions["braw1"]; s.ApprovalsEnabled {
		t.Error("braw1: ApprovalsEnabled should be cleared after migration")
	}

	if s := loaded.Sessions["canny1"]; s.AgentHooks {
		t.Error("canny1: AgentHooks = true, want false (was never set)")
	}

	if s := loaded.Sessions["kirk1"]; !s.AgentHooks {
		t.Error("kirk1: AgentHooks = false, want true (was already set)")
	}
}

func TestMigrateApprovalsEnabledBothFieldsSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	data := []byte(`{"version":3,"sessions":{
		"braw-both":{"id":"braw-both","name":"braw-both","status":"running","approvals_enabled":true,"agent_hooks":true},
		"thrawn-clash":{"id":"thrawn-clash","name":"thrawn-clash","status":"running","approvals_enabled":true,"agent_hooks":false}
	}}`)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if s := loaded.Sessions["braw-both"]; !s.AgentHooks || s.ApprovalsEnabled {
		t.Errorf("braw-both: AgentHooks=%v ApprovalsEnabled=%v, want true/false", s.AgentHooks, s.ApprovalsEnabled)
	}

	if s := loaded.Sessions["thrawn-clash"]; !s.AgentHooks || s.ApprovalsEnabled {
		t.Errorf("thrawn-clash: AgentHooks=%v ApprovalsEnabled=%v, want true/false", s.AgentHooks, s.ApprovalsEnabled)
	}
}

func TestMigrateApprovalsEnabledFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	data := []byte(`{"version":1,"sessions":{
		"auld1":{"id":"auld1","name":"auld-kirk","status":"running","approvals_enabled":true}
	}}`)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d", loaded.Version, CurrentStateVersion)
	}

	s := loaded.Sessions["auld1"]
	if !s.AgentHooks {
		t.Error("AgentHooks = false, want true (migrated from v1 approvals_enabled)")
	}

	if s.ApprovalsEnabled {
		t.Error("ApprovalsEnabled should be cleared after migration")
	}
}

func TestReconcileDeletingRevertedToStopped(t *testing.T) {
	state := &State{
		Sessions: map[string]*SessionState{
			"fash-del": {
				ID: "fash-del", Name: "thrawn-stuck", Status: StatusDeleting,
			},
			"braw-run": {
				ID: "braw-run", Name: "bonnie-alive", Status: StatusRunning, PID: 99999999,
			},
		},
	}
	state.Reconcile()

	if state.Sessions["fash-del"].Status != StatusStopped {
		t.Errorf("deleting session status = %q, want %q", state.Sessions["fash-del"].Status, StatusStopped)
	}
}

func TestStatusDeletingPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	state := &State{
		Sessions: map[string]*SessionState{
			"braw1": {
				ID: "braw1", Name: "fash-session", Status: StatusDeleting,
				CreatedAt: time.Now().UTC(),
			},
		},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	s := loaded.Sessions["braw1"]
	if s.Status != StatusDeleting {
		t.Errorf("status = %q, want %q", s.Status, StatusDeleting)
	}
}

func TestLoadStateV4MigratesStatusChangedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	createdAt := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	data := []byte(`{"version":4,"sessions":{
		"braw1":{"id":"braw1","name":"auld-kirk","status":"running","created_at":"` + createdAt.Format(time.RFC3339Nano) + `"},
		"canny1":{"id":"canny1","name":"haar-session","status":"stopped"}
	}}`)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d", loaded.Version, CurrentStateVersion)
	}

	s1 := loaded.Sessions["braw1"]
	if s1.StatusChangedAt.IsZero() {
		t.Error("braw1: StatusChangedAt is zero, want backfilled from CreatedAt")
	}

	if !s1.StatusChangedAt.Equal(createdAt) {
		t.Errorf("braw1: StatusChangedAt = %v, want %v (CreatedAt)", s1.StatusChangedAt, createdAt)
	}

	s2 := loaded.Sessions["canny1"]
	if !s2.StatusChangedAt.IsZero() && !s2.StatusChangedAt.Equal(s2.CreatedAt) {
		t.Errorf("canny1: StatusChangedAt = %v, want zero or equal to CreatedAt (%v)", s2.StatusChangedAt, s2.CreatedAt)
	}
}

func TestMigrateV6ToV7(t *testing.T) {
	state := &State{
		Version: 6,
		Sessions: map[string]*SessionState{
			"braw1": {
				ID:   "braw1",
				Name: "neep-kirk",
			},
		},
	}
	if err := migrateState(state); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	if state.Version != CurrentStateVersion {
		t.Errorf("expected version %d, got %d", CurrentStateVersion, state.Version)
	}

	s := state.Sessions["braw1"]
	if s.SummaryText != "" {
		t.Errorf("expected empty SummaryText, got %q", s.SummaryText)
	}

	if s.SummarySetAt != nil {
		t.Errorf("expected nil SummarySetAt")
	}
}

func TestSandboxConfigNilBackwardCompat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	data := []byte(`{"version":1,"sessions":{"braw1":{"id":"braw1","name":"auld-kirk","status":"stopped","sandboxed":true}}}`)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	s := loaded.Sessions["braw1"]
	if !s.Sandboxed {
		t.Error("Sandboxed = false, want true")
	}

	if s.SandboxConfig != nil {
		t.Error("SandboxConfig should be nil for pre-existing state without the field")
	}
}
