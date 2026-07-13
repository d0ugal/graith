package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestMigrateV13ToV14SoftDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// A v13 state with no deleted_at/expires_at fields.
	data := []byte(`{"version":13,"sessions":{
		"braw1":{"id":"braw1","name":"bide-session","status":"stopped"}
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

	s := loaded.Sessions["braw1"]
	if s == nil {
		t.Fatal("braw1 session lost during v13→v14 migration")
	}

	if s.DeletedAt != nil || s.ExpiresAt != nil {
		t.Error("v13 session should migrate to v14 with nil DeletedAt/ExpiresAt (live)")
	}

	if s.IsSoftDeleted() {
		t.Error("migrated session should not be soft-deleted")
	}
}

// TestMigrateV16ToV17PromptedAuthors covers the author-trust gate's persisted
// set (issue #1039): a v16 state with no pr_watch_prompted_authors field must
// migrate to an initialised empty map, leaving pre-existing sessions untouched.
func TestMigrateV16ToV17PromptedAuthors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// A v16 state with no pr_watch_prompted_authors field.
	data := []byte(`{"version":16,"sessions":{
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

	if loaded.PRWatchPromptedAuthors == nil {
		t.Fatal("PRWatchPromptedAuthors is nil after migration; want an initialized map")
	}

	if len(loaded.PRWatchPromptedAuthors) != 0 {
		t.Errorf("PRWatchPromptedAuthors has %d entries, want 0", len(loaded.PRWatchPromptedAuthors))
	}

	if s := loaded.Sessions["braw1"]; s == nil || s.Name != "bide-session" {
		t.Error("braw1 session lost or altered during v16→v17 migration")
	}
}

func TestMigrateV17ToV18DriverKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// A v17 state with no driver_kind field — every existing session is a PTY.
	data := []byte(`{"version":17,"sessions":{
		"braw1":{"id":"braw1","name":"bide-session","status":"running"},
		"canny2":{"id":"canny2","name":"ken-session","status":"stopped"}
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

	for id, s := range loaded.Sessions {
		if s.DriverKind != DriverPTY {
			t.Errorf("session %q DriverKind = %q, want %q after v17→v18 migration", id, s.DriverKind, DriverPTY)
		}
	}
}

// TestLoadStatePromptedAuthorsRoundTrip asserts a populated prompted-authors set
// survives a save/load cycle.
func TestLoadStatePromptedAuthorsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	st := NewState()
	st.PRWatchPromptedAuthors["scunner"] = true
	st.PRWatchPromptedAuthors["dreich"] = true

	if err := SaveState(path, st); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if !loaded.PRWatchPromptedAuthors["scunner"] || !loaded.PRWatchPromptedAuthors["dreich"] {
		t.Errorf("prompted authors should round-trip, got %v", loaded.PRWatchPromptedAuthors)
	}
}

func TestLoadStateV14RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// A v14 state with an explicit soft-deleted session.
	data := []byte(`{"version":14,"sessions":{
		"dreich1":{"id":"dreich1","name":"dreich","status":"stopped",
			"deleted_at":"2026-07-10T06:53:00Z","expires_at":"2026-07-11T06:53:00Z"}
	}}`)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	s := loaded.Sessions["dreich1"]
	if s == nil || !s.IsSoftDeleted() {
		t.Fatal("soft-deleted session did not round-trip through v14 load")
	}

	if s.ExpiresAt == nil {
		t.Error("ExpiresAt lost on v14 load")
	}
}

// TestLoadStateNewerVersionRejected is the downgrade fail-closed guard: a state
// file newer than this binary must be rejected with a typed StateVersionError,
// not silently discarded (which would orphan running agents).
func TestLoadStateNewerVersionRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	data := []byte(`{"version":9999,"sessions":{}}`)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}

	_, err := LoadState(path)
	if err == nil {
		t.Fatal("expected an error loading a newer-than-binary state file")
	}

	var ve *StateVersionError
	if !errors.As(err, &ve) {
		t.Fatalf("error %v is not a *StateVersionError", err)
	}

	if ve.FileVersion != 9999 || ve.BinaryVersion != CurrentStateVersion {
		t.Errorf("StateVersionError = {file:%d binary:%d}, want {9999 %d}",
			ve.FileVersion, ve.BinaryVersion, CurrentStateVersion)
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

// oldStateData builds a state file JSON at the given version with one session,
// for exercising the pre-migration backup.
func oldStateData(t *testing.T, version int) []byte {
	t.Helper()

	data, err := json.Marshal(map[string]any{
		"version": version,
		"sessions": map[string]any{
			"braw1": map[string]any{
				"id":     "braw1",
				"name":   "auld-kirk",
				"status": "stopped",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestLoadStateBacksUpBeforeMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	oldVersion := CurrentStateVersion - 1
	before := oldStateData(t, oldVersion)

	if err := writeFileAtomic(path, before); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if state.Version != CurrentStateVersion {
		t.Fatalf("loaded version = %d, want %d after migration", state.Version, CurrentStateVersion)
	}

	backup := StateBackupPath(path, oldVersion)

	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("backup not written at %s: %v", backup, err)
	}

	if !bytes.Equal(got, before) {
		t.Errorf("backup content = %q, want the pre-migration bytes %q", got, before)
	}

	// The backup must hold the OLD version, not the migrated one.
	var backedUp State
	if err := json.Unmarshal(got, &backedUp); err != nil {
		t.Fatal(err)
	}

	if backedUp.Version != oldVersion {
		t.Errorf("backup version = %d, want %d (pre-migration)", backedUp.Version, oldVersion)
	}
}

func TestLoadStateNoBackupWhenCurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	data := oldStateData(t, CurrentStateVersion)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(path); err != nil {
		t.Fatal(err)
	}

	if backups := ListStateBackups(path); len(backups) != 0 {
		t.Errorf("no backup expected when state is already current, got %v", backups)
	}
}

func TestLoadStateNoBackupWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if _, err := LoadState(path); err != nil {
		t.Fatal(err)
	}

	if backups := ListStateBackups(path); len(backups) != 0 {
		t.Errorf("no backup expected for a missing state file, got %v", backups)
	}
}

func TestLoadStateReplacesStaleBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// A leftover backup from an earlier migration.
	staleVersion := CurrentStateVersion - 3
	stale := StateBackupPath(path, staleVersion)

	if err := writeFileAtomic(stale, []byte(`{"version":1,"stale":true}`)); err != nil {
		t.Fatal(err)
	}

	oldVersion := CurrentStateVersion - 1
	if err := writeFileAtomic(path, oldStateData(t, oldVersion)); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(path); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale backup %s should have been removed, stat err = %v", stale, err)
	}

	backups := ListStateBackups(path)
	if len(backups) != 1 {
		t.Fatalf("want exactly one backup after migration, got %v", backups)
	}

	if backups[0] != StateBackupPath(path, oldVersion) {
		t.Errorf("kept backup = %s, want %s", backups[0], StateBackupPath(path, oldVersion))
	}
}

func TestLoadStateBackupCrashSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	oldVersion := CurrentStateVersion - 1
	if err := writeFileAtomic(path, oldStateData(t, oldVersion)); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(path); err != nil {
		t.Fatal(err)
	}

	// atomicfile must leave no stray temp files behind in the data dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("stray temp file left behind: %s", e.Name())
		}
	}
}

func TestListStateBackupsMatching(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Files that should be recognised as backups...
	good := []string{
		StateBackupPath(path, 0),
		StateBackupPath(path, 16),
	}
	// ...and files that should not.
	bad := []string{
		filepath.Join(dir, "state.json"),
		filepath.Join(dir, "state.json.bak"),      // no version segment
		filepath.Join(dir, "state.json.vfoo.bak"), // non-numeric
		filepath.Join(dir, "state.json.v16.txt"),  // wrong suffix
		filepath.Join(dir, "other.json.v1.bak"),   // different base
	}

	for _, p := range append(append([]string{}, good...), bad...) {
		if err := writeFileAtomic(p, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}

	got := ListStateBackups(path)
	if len(got) != len(good) {
		t.Fatalf("ListStateBackups = %v, want %v", got, good)
	}

	for i, want := range good {
		if got[i] != want {
			t.Errorf("backup[%d] = %s, want %s", i, got[i], want)
		}
	}
}

func TestLoadStateNoBackupOnCorruptedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Invalid JSON is handled before the version/backup block: LoadState starts
	// fresh and writes no backup. Documents (and guards) that deliberate order —
	// the backup call must never move above the unmarshal.
	if err := writeFileAtomic(path, []byte("not json")); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(path); err != nil {
		t.Fatal(err)
	}

	if backups := ListStateBackups(path); len(backups) != 0 {
		t.Errorf("no backup expected for corrupted state, got %v", backups)
	}
}

func TestLoadStateBackupFailureNonFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	oldVersion := CurrentStateVersion - 1
	if err := writeFileAtomic(path, oldStateData(t, oldVersion)); err != nil {
		t.Fatal(err)
	}

	// A read-only data dir lets LoadState read state.json but makes the sibling
	// backup write fail (atomicfile can't create its temp file). LoadState must
	// still migrate and return successfully — the backup is best-effort.
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: read-only dir is the point of this failure test
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(dir, 0o750) }) //nolint:gosec // G302: restore perms so t.TempDir cleanup can traverse

	state, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState must not fail when the backup can't be written: %v", err)
	}

	if state.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d — migration must still run", state.Version, CurrentStateVersion)
	}

	if s, ok := state.Sessions["braw1"]; !ok || s.Name != "auld-kirk" {
		t.Error("session lost when backup failed")
	}

	if backups := ListStateBackups(path); len(backups) != 0 {
		t.Errorf("no backup expected after a failed backup write, got %v", backups)
	}
}

func TestLoadStateBackupPreservedWhenMigrationFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	oldVersion := CurrentStateVersion - 1
	before := oldStateData(t, oldVersion)

	if err := writeFileAtomic(path, before); err != nil {
		t.Fatal(err)
	}

	// Force the migration for this version to fail. Because the backup is
	// written BEFORE migrateState runs, the pre-migration state must survive on
	// disk even though LoadState starts fresh — this is the "rescue point if a
	// forward migration corrupts state" guarantee, and it locks the ordering.
	orig := migrations[oldVersion]
	migrations[oldVersion] = func(*State) error { return errors.New("dreich migration") }

	t.Cleanup(func() { migrations[oldVersion] = orig })

	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	// A failed migration starts fresh (empty state at the current version).
	if len(state.Sessions) != 0 {
		t.Errorf("expected fresh empty state after failed migration, got %d sessions", len(state.Sessions))
	}

	// ...but the backup preserves the exact pre-migration bytes.
	got, err := os.ReadFile(StateBackupPath(path, oldVersion))
	if err != nil {
		t.Fatalf("backup should survive a failed migration: %v", err)
	}

	if !bytes.Equal(got, before) {
		t.Errorf("backup content = %q, want pre-migration bytes %q", got, before)
	}
}
