//go:build libghostty && cgo && ((darwin && arm64) || linux)

package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	grpty "github.com/d0ugal/graith/internal/pty"
)

const (
	libghosttyUpgradeStageEnv = "GRAITH_TEST_LIBGHOSTTY_UPGRADE_STAGE"
	libghosttyUpgradeDirEnv   = "GRAITH_TEST_LIBGHOSTTY_UPGRADE_DIR"
)

func TestLibghosttyExecUpgradeReapsHelpersAndReconstructsScreen(t *testing.T) {
	switch os.Getenv(libghosttyUpgradeStageEnv) {
	case "old":
		runOldLibghosttyUpgradeStage(t)
		return
	case "new":
		runNewLibghosttyUpgradeStage(t)
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestLibghosttyExecUpgradeReapsHelpersAndReconstructsScreen$")
	cmd.Env = replaceTestEnv(os.Environ(), libghosttyUpgradeStageEnv, "old")
	cmd.Env = replaceTestEnv(cmd.Env, libghosttyUpgradeDirEnv, t.TempDir())
	output := &boundedUpgradeTestOutput{remaining: 32 * 1024}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		t.Fatalf("tagged exec upgrade failed: %v; diagnostic bytes captured: %d", err, output.Len())
	}
}

func replaceTestEnv(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}

	return append(result, prefix+value)
}

type boundedUpgradeTestOutput struct {
	buffer    bytes.Buffer
	remaining int
}

func (w *boundedUpgradeTestOutput) Write(p []byte) (int, error) {
	original := len(p)
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}
	_, _ = w.buffer.Write(p)
	w.remaining -= len(p)

	return original, nil
}

func (w *boundedUpgradeTestOutput) Len() int { return w.buffer.Len() }

func runOldLibghosttyUpgradeStage(t *testing.T) {
	dir := os.Getenv(libghosttyUpgradeDirEnv)
	session, err := grpty.NewSession(grpty.SessionOpts{
		ID: "canny-exec", Command: "sh", Args: []string{"-c", "printf 'canny-upgrade\\n'; sleep 30"},
		Rows: 8, Cols: 40, LogPath: filepath.Join(dir, "canny-exec.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(session.ScreenPreview(), "canny-upgrade") {
		if time.Now().After(deadline) {
			t.Fatal("screen marker did not render before exec")
		}
		time.Sleep(10 * time.Millisecond)
	}
	startTime, err := grpty.ProcessStartTime(session.ProcessPID())
	if err != nil {
		t.Fatal(err)
	}
	helpers, err := grpty.FreezeTerminalHelpers(context.Background())
	if err != nil || len(helpers) != 1 {
		t.Fatalf("helper freeze = (%+v, %v)", helpers, err)
	}
	listenerR, listenerW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer listenerW.Close()
	listenerFD := duplicateTransferredFileFD(t, listenerR)
	if err := setDescriptorFlags(listenerFD, 0); err != nil {
		t.Fatal(err)
	}
	handoffFD, err := session.DuplicateFD()
	if err != nil {
		t.Fatal(err)
	}
	if err := setDescriptorFlags(handoffFD, 0); err != nil {
		t.Fatal(err)
	}
	scrollbackFD, err := session.Scrollback.DuplicateFD()
	if err != nil {
		t.Fatal(err)
	}
	if err := setDescriptorFlags(scrollbackFD, 0); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "config.toml")
	manifest := &UpgradeManifest{
		Version:       upgradeManifestVersion,
		ListenerFd:    listenerFD,
		StateSnapshot: []byte(`{"version":23,"sessions":{}}`),
		ConfigFile:    configFile,
		Paths: UpgradePathDescriptor{
			ConfigFile: configFile,
			DataDir:    dir,
			StateFile:  filepath.Join(dir, "state.json"),
			RuntimeDir: dir,
			SocketPath: filepath.Join(dir, "graith.sock"),
		},
		Sessions: []UpgradeSession{{
			ID: session.ID, Fd: handoffFD, ScrollbackFd: scrollbackFD,
			PID: session.ProcessPID(), PIDStartTime: startTime,
		}},
		Target: UpgradeTargetDescriptor{
			ResolvedPath: executable, Size: info.Size(), Mode: uint32(info.Mode()),
			ModTimeNanos: info.ModTime().UnixNano(), SHA256: mustDigestFile(t, executable),
		},
	}
	for _, helper := range helpers {
		manifest.Helpers = append(manifest.Helpers, UpgradeHelper{PID: helper.PID, StartTime: helper.StartTime})
	}
	manifestPath, err := WriteManifest(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	env := replaceTestEnv(os.Environ(), libghosttyUpgradeStageEnv, "new")
	env = replaceTestEnv(env, libghosttyUpgradeDirEnv, dir)
	env = replaceTestEnv(env, "GRAITH_TEST_LIBGHOSTTY_MANIFEST", manifestPath)
	if err := syscall.Exec(executable, []string{executable, "-test.run=^TestLibghosttyExecUpgradeReapsHelpersAndReconstructsScreen$"}, env); err != nil {
		t.Fatal(err)
	}
}

func mustDigestFile(t *testing.T, path string) string {
	t.Helper()
	digest, err := digestFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return digest
}

func runNewLibghosttyUpgradeStage(t *testing.T) {
	manifest, err := ReadManifest(os.Getenv("GRAITH_TEST_LIBGHOSTTY_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	if err := newUpgradeOwnershipGuard(manifest).reapHelpers(); err != nil {
		t.Fatal(err)
	}
	for _, helper := range manifest.Helpers {
		var status syscall.WaitStatus
		_, err := syscall.Wait4(helper.PID, &status, syscall.WNOHANG, nil)
		if !errors.Is(err, syscall.ECHILD) {
			t.Fatalf("helper %d remains waitable after reap: %v", helper.PID, err)
		}
	}
	_ = syscall.Close(manifest.ListenerFd)
	entry := manifest.Sessions[0]
	session, err := grpty.AdoptSession(grpty.AdoptOpts{
		ID: entry.ID, Fd: uintptr(entry.Fd), ScrollbackFd: uintptr(entry.ScrollbackFd), PID: entry.PID,
		ExpectedPIDStartTime: entry.PIDStartTime,
		LogPath:              filepath.Join(os.Getenv(libghosttyUpgradeDirEnv), "canny-exec.log"),
		HydrationBytes:       1024 * 1024,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview := session.ScreenPreview(); !strings.Contains(preview, "canny-upgrade") {
		t.Fatal("adopted screen was not reconstructed")
	}
	_ = session.ForceKill()
	select {
	case <-session.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("adopted command did not exit")
	}
	session.Close()
}
