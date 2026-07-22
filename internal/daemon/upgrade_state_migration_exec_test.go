package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

const (
	execStateMigrationStageEnv    = "GRAITH_TEST_EXEC_STATE_MIGRATION_STAGE"
	execStateMigrationDirEnv      = "GRAITH_TEST_EXEC_STATE_MIGRATION_DIR"
	execStateMigrationManifestEnv = "GRAITH_TEST_EXEC_STATE_MIGRATION_MANIFEST"
	execStateMigrationTestName    = "TestExecAdoptionMigratesOlderStateAndPreservesPTY"
)

func TestExecAdoptionMigratesOlderStateAndPreservesPTY(t *testing.T) {
	switch os.Getenv(execStateMigrationStageEnv) {
	case "old":
		runOldExecStateMigrationStage(t)
		return
	case "new":
		runNewExecStateMigrationStage(t)
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, executable, "-test.run=^"+execStateMigrationTestName+"$")
	cmd.Env = replaceExecStateMigrationEnv(os.Environ(), execStateMigrationStageEnv, "old")
	cmd.Env = replaceExecStateMigrationEnv(cmd.Env, execStateMigrationDirEnv, t.TempDir())

	output := &boundedExecStateMigrationOutput{remaining: 16 * 1024}
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Run(); err != nil {
		t.Fatalf("exec state migration failed: %v; output=%q", err, output.String())
	}
}

func runOldExecStateMigrationStage(t *testing.T) {
	const sessionID = "canny-migration"

	dir := os.Getenv(execStateMigrationDirEnv)
	logPath := filepath.Join(dir, sessionID+".log")

	session, err := grpty.NewSession(grpty.SessionOpts{
		ID:      sessionID,
		Command: "sh",
		Args:    []string{"-c", "printf 'canny-before-exec\\n'; while IFS= read -r line; do printf 'seen:%s\\n' \"$line\"; done"},
		Rows:    8, Cols: 48, LogPath: logPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	execSucceeded := false
	defer func() {
		if execSucceeded {
			return
		}

		_ = session.ForceKill()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
		}

		session.Close()
	}()

	waitForExecStateMigrationPreview(t, session, "canny-before-exec")

	startTime, err := grpty.ProcessStartTime(session.ProcessPID())
	if err != nil {
		t.Fatal(err)
	}

	handoffFD, err := session.DuplicateFD()
	if err != nil {
		t.Fatal(err)
	}

	scrollbackFD, err := session.Scrollback.DuplicateFD()
	if err != nil {
		_ = syscall.Close(handoffFD)

		t.Fatal(err)
	}

	for _, fd := range []int{handoffFD, scrollbackFD} {
		if err := setDescriptorFlags(fd, 0); err != nil {
			t.Fatal(err)
		}
	}

	quiesceCtx, cancelQuiesce := context.WithTimeout(context.Background(), 5*time.Second)

	release, err := session.QuiesceIOForUpgrade(quiesceCtx)

	cancelQuiesce()

	if err != nil {
		t.Fatal(err)
	}

	defer release()

	stateSnapshot, err := json.Marshal(map[string]any{
		"version": 23,
		"sessions": map[string]any{
			sessionID: map[string]any{
				"id": sessionID, "name": "canny-migration", "agent": "canny-shell",
				"worktree_path": dir, "status": StatusRunning,
				"status_changed_at": time.Now().UTC(),
				"pid":               session.ProcessPID(), "pid_start_time": startTime,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(dir, "state.json")
	if err := writeFileAtomic(statePath, stateSnapshot); err != nil {
		t.Fatal(err)
	}

	manifest := &UpgradeManifest{
		Version:       upgradeManifestVersion,
		StateSnapshot: stateSnapshot,
		Sessions: []UpgradeSession{{
			ID: sessionID, Fd: handoffFD, HasPTY: true, ScrollbackFd: scrollbackFD,
			PID: session.ProcessPID(), PIDStartTime: startTime,
		}},
	}

	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(dir, "upgrade-manifest.json")
	if err := writeFileAtomic(manifestPath, manifestData); err != nil {
		t.Fatal(err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	env := replaceExecStateMigrationEnv(os.Environ(), execStateMigrationStageEnv, "new")
	env = replaceExecStateMigrationEnv(env, execStateMigrationManifestEnv, manifestPath)
	execSucceeded = true

	if err := syscall.Exec(executable, []string{executable, "-test.run=^" + execStateMigrationTestName + "$"}, env); err != nil {
		execSucceeded = false

		t.Fatal(err)
	}
}

func runNewExecStateMigrationStage(t *testing.T) {
	const sessionID = "canny-migration"

	dir := os.Getenv(execStateMigrationDirEnv)
	manifestPath := os.Getenv(execStateMigrationManifestEnv)

	manifestData, err := os.ReadFile(manifestPath) //nolint:gosec // G703: parent test supplies this path in its private temp directory.
	if err != nil {
		t.Fatal(err)
	}

	var manifest UpgradeManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}

	paths := config.Paths{
		StateFile: filepath.Join(dir, "state.json"),
		DataDir:   dir, LogDir: dir, RuntimeDir: dir, TmpDir: filepath.Join(dir, "tmp"),
	}
	sm := NewSessionManager(
		config.Default(), paths,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	if _, err := backupStateSnapshotBeforeMigration(paths.StateFile, manifest.StateSnapshot); err != nil {
		t.Fatal(err)
	}

	originalVersion, err := sm.loadStateSnapshotForAdoption(manifest.StateSnapshot)
	if err != nil {
		t.Fatal(err)
	}

	if originalVersion != 23 {
		t.Fatalf("original state version = %d, want 23", originalVersion)
	}

	migrated := sm.state.Sessions[sessionID]
	if sm.state.Version != CurrentStateVersion || migrated == nil {
		t.Fatalf("migrated state = version %d session=%+v", sm.state.Version, migrated)
	}

	if sm.state.UpgradeCleanup == nil {
		t.Fatal("v23 to v24 migration did not initialize upgrade cleanup ownership")
	}

	if migrated.CWD != dir {
		t.Fatalf("migrated cwd = %q, want %q", migrated.CWD, dir)
	}

	if migrated.Labels == nil {
		t.Fatal("migration did not initialize the session label set")
	}

	result, err := sm.adoptSessions(&manifest, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.ResolvedSessions) != 1 || len(result.UnresolvedSessions) != 0 ||
		len(result.adoptedSessions) != 1 {
		t.Fatalf("adoption result = %+v, want one resolved live PTY", result)
	}

	if err := sm.saveState(); err != nil {
		t.Fatal(err)
	}

	backup, err := os.ReadFile(StateBackupPath(paths.StateFile, originalVersion))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(backup, manifest.StateSnapshot) {
		t.Fatal("pre-migration adoption backup does not match the handed-off state snapshot")
	}

	persisted, err := LoadState(paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	if persisted.Version != CurrentStateVersion || persisted.Sessions[sessionID] == nil {
		t.Fatalf("persisted adopted state = version %d session=%+v", persisted.Version, persisted.Sessions[sessionID])
	}

	adopted := result.adoptedSessions[0]
	adopted.StartAdoptedWaiter()

	if err := adopted.WriteInputAndSubmit([]byte("thrawn-after-exec")); err != nil {
		t.Fatal(err)
	}

	waitForExecStateMigrationLog(t, filepath.Join(dir, sessionID+".log"), "thrawn-after-exec")

	if err := adopted.ForceKill(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-adopted.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("adopted session did not exit")
	}

	adopted.Close()
}

func waitForExecStateMigrationLog(t *testing.T, path, marker string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for {
		data, err := os.ReadFile(path) //nolint:gosec // G703: caller passes the parent test's private temp log path.
		if err == nil && bytes.Contains(data, []byte(marker)) {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("session log did not contain %q: %q, err=%v", marker, data, err)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func waitForExecStateMigrationPreview(t *testing.T, session *grpty.Session, marker string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(session.ScreenPreview(), marker) {
		if time.Now().After(deadline) {
			t.Fatalf("session preview did not contain %q", marker)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func replaceExecStateMigrationEnv(env []string, key, value string) []string {
	prefix := key + "="

	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}

	return append(result, prefix+value)
}

type boundedExecStateMigrationOutput struct {
	bytes.Buffer

	remaining int
}

func (w *boundedExecStateMigrationOutput) Write(p []byte) (int, error) {
	original := len(p)
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}

	_, _ = w.Buffer.Write(p)
	w.remaining -= len(p)

	return original, nil
}

func (w *boundedExecStateMigrationOutput) String() string {
	return w.Buffer.String()
}
