package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
	"golang.org/x/sys/unix"
)

func TestWriteAndReadManifest(t *testing.T) {
	dir := t.TempDir()

	original := &UpgradeManifest{
		ListenerFd: 5,
		ConfigFile: "/home/user/.config/graith/config.toml",
		Sessions: []UpgradeSession{
			{ID: "abc123", Fd: 10, PID: 1234},
			{ID: "def456", Fd: 11, PID: 5678},
		},
	}

	path, err := WriteManifest(dir, original)
	if err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}

	wantPath := filepath.Join(dir, "upgrade-adoption-"+original.JournalID+".pending")
	if path != wantPath {
		t.Errorf("WriteManifest() path = %q, want %q", path, wantPath)
	}

	loaded, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}

	if loaded.ListenerFd != original.ListenerFd {
		t.Errorf("ListenerFd = %d, want %d", loaded.ListenerFd, original.ListenerFd)
	}

	if loaded.ConfigFile != original.ConfigFile {
		t.Errorf("ConfigFile = %q, want %q", loaded.ConfigFile, original.ConfigFile)
	}

	if len(loaded.Sessions) != len(original.Sessions) {
		t.Fatalf("Sessions len = %d, want %d", len(loaded.Sessions), len(original.Sessions))
	}

	for i, s := range loaded.Sessions {
		orig := original.Sessions[i]
		if s.ID != orig.ID || s.Fd != orig.Fd || s.PID != orig.PID {
			t.Errorf("Sessions[%d] = %+v, want %+v", i, s, orig)
		}
	}
}

func TestWriteManifestEmptySessions(t *testing.T) {
	dir := t.TempDir()

	original := &UpgradeManifest{
		ListenerFd: 3,
		ConfigFile: "",
		Sessions:   nil,
	}

	path, err := WriteManifest(dir, original)
	if err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}

	loaded, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}

	if loaded.ListenerFd != 3 {
		t.Errorf("ListenerFd = %d, want 3", loaded.ListenerFd)
	}

	if len(loaded.Sessions) != 0 {
		t.Errorf("Sessions len = %d, want 0", len(loaded.Sessions))
	}
}

func TestReadManifestNonExistent(t *testing.T) {
	_, err := ReadManifest("/nonexistent/manifest.json")
	if err == nil {
		t.Fatal("expected error for nonexistent manifest file")
	}
}

func TestUpgradeOwnershipCapsuleRoundTripScrubsPrivateEnvironment(t *testing.T) {
	manifest := &UpgradeManifest{
		Version: upgradeManifestVersion, ListenerFd: 7,
		StateSnapshot: []byte(`{"version":23,"sessions":{}}`),
		Sessions:      []UpgradeSession{{ID: "canny", Fd: 9, ScrollbackFd: 10, PID: 1234, PIDStartTime: 5678}},
		Helpers:       []UpgradeHelper{{PID: 4321, StartTime: 8765}},
	}
	manifest.journalSHA256 = sha256.Sum256([]byte("braw journal"))
	if err := prepareOwnershipCapsule(manifest); err != nil {
		t.Fatal(err)
	}
	t.Setenv(upgradeOwnershipCapsuleEnv, manifest.ownershipCapsule)
	t.Setenv(upgradeOwnershipFDEnv, "11")
	capsuleRaw, fdRaw := captureUpgradeBootstrapEnvironment()
	if os.Getenv(upgradeOwnershipCapsuleEnv) != "" || os.Getenv(upgradeOwnershipFDEnv) != "" {
		t.Fatal("private upgrade environment remained visible after bootstrap capture")
	}
	if fdRaw != "11" {
		t.Fatalf("captured ownership descriptor = %q", fdRaw)
	}
	owned, err := readInheritedOwnershipCapsule(capsuleRaw)
	if err != nil {
		t.Fatal(err)
	}
	if !upgradeOwnershipResourcesMatch(manifest, owned) {
		t.Fatalf("decoded capsule resources = %+v, want %+v", owned, manifest)
	}
}

func TestExecUpgradeRefusesCapsulelessDirectReplacement(t *testing.T) {
	err := ExecUpgrade("/braw/manifest", "/braw/config", "/braw/gr")
	if err == nil || !strings.Contains(err.Error(), "negotiated daemon upgrade protocol") {
		t.Fatalf("ExecUpgrade error = %v, want safe direct-call refusal", err)
	}
}

func TestUpgradeOwnershipCapsuleExactCapacityRefusesUnsafeExecBudget(t *testing.T) {
	manifest := &UpgradeManifest{Version: upgradeManifestVersion, ListenerFd: 7}
	manifest.journalSHA256 = sha256.Sum256([]byte("canny journal"))
	for i := 0; i < upgradeManifestMaxSessions; i++ {
		manifest.Sessions = append(manifest.Sessions, UpgradeSession{
			ID: fmt.Sprintf("croft-%d", i), Fd: i + 10, ScrollbackFd: i + 5000,
			PID: i + 10_000, PIDStartTime: int64(i + 20_000),
		})
	}
	if err := prepareOwnershipCapsule(manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.ownershipCapsule) >= upgradeExecEnvironmentMax {
		t.Fatalf("encoded exact-capacity capsule is too large: %d", len(manifest.ownershipCapsule))
	}
	t.Setenv("GRAITH_TEST_CAPSULE_BUDGET", strings.Repeat("b", 70_000))
	err := validateUpgradeExecBudget(&upgradeTarget{path: "/braw/gr"}, "/braw/manifest", "", 8, manifest.ownershipCapsule)
	if err == nil {
		t.Fatal("oversized full environment was accepted")
	}
}

func TestUpgradeDescriptorBudgetAccountsForTwoFDsPerSession(t *testing.T) {
	if err := validateUpgradeDescriptorBudgetValues(256, 100, 64); err != nil {
		t.Fatalf("native 64-session descriptor budget rejected: %v", err)
	}
	if err := validateUpgradeDescriptorBudgetValues(243, 100, 64); err == nil {
		t.Fatal("two descriptors per session plus headroom exceeded RLIMIT without refusal")
	}
	if err := validateUpgradeDescriptorBudgetValues(4096, 100, upgradeManifestMaxSessions); err == nil {
		t.Fatal("unlimited fallback ignored descriptor budget")
	}
}

func TestWriteManifestDirectorySyncFailureRemovesJournal(t *testing.T) {
	dir := t.TempDir()
	originalSync := syncUpgradeManifestDirectory
	t.Cleanup(func() { syncUpgradeManifestDirectory = originalSync })
	calls := 0
	syncUpgradeManifestDirectory = func(string) error {
		calls++
		if calls == 1 {
			return errors.New("dreich directory")
		}

		return nil
	}
	path, err := WriteManifest(dir, &UpgradeManifest{ListenerFd: 7, Sessions: nil})
	if err == nil || path != "" {
		t.Fatalf("WriteManifest = (%q, %v), want durable refusal", path, err)
	}
	if pending, listErr := upgradeJournalPaths(dir); listErr != nil || len(pending) != 0 {
		t.Fatalf("failed manifest commit remains visible: paths=%v err=%v", pending, listErr)
	}
}

func TestWriteManifestPossiblyPublishedFailureReturnsExactPathForRollbackPhase(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	manifest := validUpgradeManifestForBoundaryTest(t, 7, []UpgradeSession{{
		ID: "braw", Fd: 9, PID: cmd.Process.Pid, PIDStartTime: start,
	}})
	manifest.Paths.RuntimeDir = canonicalUpgradePath(dir)
	originalSync := syncUpgradeManifestDirectory
	originalRemove := removeUpgradePublishedPath
	t.Cleanup(func() {
		syncUpgradeManifestDirectory = originalSync
		removeUpgradePublishedPath = originalRemove
	})
	syncUpgradeManifestDirectory = func(string) error { return errors.New("dreich sync") }
	removeUpgradePublishedPath = func(string) error { return errors.New("dreich remove") }
	path, writeErr := WriteManifest(dir, manifest)
	if writeErr == nil || path == "" {
		t.Fatalf("possibly-published WriteManifest = (%q, %v), want exact path plus error", path, writeErr)
	}
	syncUpgradeManifestDirectory = originalSync
	removeUpgradePublishedPath = originalRemove
	if err := writeUpgradeJournalMarker(path, manifest, upgradeJournalRolledBack); err != nil {
		t.Fatal(err)
	}
	if err := recoverPendingUpgradeJournals(dir); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("rollback-phased publication failure signaled live process: %v", err)
	}
}

func TestWriteManifestPublicationIsGloballyExclusiveAndNoReplace(t *testing.T) {
	dir := t.TempDir()
	start := make(chan struct{})
	type result struct {
		path string
		err  error
	}
	results := make(chan result, 2)
	for _, fd := range []int{7, 9} {
		manifest := &UpgradeManifest{ListenerFd: fd}
		go func() {
			<-start
			path, err := WriteManifest(dir, manifest)
			results <- result{path: path, err: err}
		}()
	}
	close(start)
	var successes int
	for range 2 {
		result := <-results
		if result.err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent publication successes = %d, want exactly one", successes)
	}
	artifacts, err := upgradeJournalPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || !strings.HasSuffix(artifacts[0], ".pending") {
		t.Fatalf("concurrent publication artifacts = %v, want one pending journal", artifacts)
	}
}

func TestUpgradeOwnershipGuardAmbiguousCloseNeverRetriesReusedNumber(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd, err := syscall.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	guard := newUpgradeOwnershipGuard(&UpgradeManifest{
		ListenerFd: -1,
		Sessions:   []UpgradeSession{{ID: "thrawn", Fd: fd, PID: os.Getpid(), PIDStartTime: 1}},
	})
	if err := guard.consumeSessionFD("thrawn", syscall.EINTR); err == nil {
		t.Fatal("ambiguous close was treated as consumed")
	}
	if err := guard.Cleanup(); err == nil {
		t.Fatal("ambiguous live descriptor was retried or forgotten")
	}
	if _, err := descriptorFlags(fd); err != nil {
		t.Fatalf("guard changed ambiguous descriptor number: %v", err)
	}
	_ = syscall.Close(fd)
}

func TestManifestHandoffMalformedBodyRetainsIndependentOwnership(t *testing.T) {
	dir := t.TempDir()
	manifest := &UpgradeManifest{
		Version: upgradeManifestVersion, ListenerFd: 7,
		StateSnapshot: []byte(`{"version":23,"sessions":{}}`),
		Sessions:      []UpgradeSession{{ID: "bothy", Fd: 9, ScrollbackFd: 10, PID: 1234, PIDStartTime: 5678}},
		Target:        UpgradeTargetDescriptor{ResolvedPath: "/braw/gr", SHA256: strings.Repeat("a", 64)},
		Paths: UpgradePathDescriptor{
			ConfigFile: "/braw/config", DataDir: "/braw/data", StateFile: "/braw/state",
			RuntimeDir: "/braw/run", SocketPath: "/braw/run/socket",
		},
		ConfigFile: "/braw/config",
	}
	path, err := WriteManifest(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareManifestHandoff(path, manifest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] = '!'
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	owned, decoded, readErr := readManifestHandoffDescriptor(manifest.ownershipFD)
	if readErr == nil || decoded != nil || owned == nil {
		t.Fatalf("malformed body = (%+v, %+v, %v), want owned cleanup plus error", owned, decoded, readErr)
	}
	if !upgradeOwnershipResourcesMatch(manifest, owned) {
		t.Fatalf("retained ownership = %+v, want manifest resources", owned)
	}
	_ = rollbackUpgradeDescriptors(manifest)
}

func TestImmutableCapsuleRejectsRewrittenManifestBody(t *testing.T) {
	dir := t.TempDir()
	manifest := validUpgradeManifestForBoundaryTest(t, 7, nil)
	path, err := WriteManifest(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareManifestHandoff(path, manifest); err != nil {
		t.Fatal(err)
	}
	if err := prepareOwnershipCapsule(manifest); err != nil {
		t.Fatal(err)
	}
	capsule, err := readInheritedOwnershipCapsule(manifest.ownershipCapsule)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := bytes.Replace(data, []byte("/braw/data"), []byte("/braw/dato"), 1)
	if bytes.Equal(rewritten, data) {
		t.Fatal("manifest mutation fixture did not change the durable body")
	}
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	header, decoded, readErr := readManifestHandoffDescriptor(manifest.ownershipFD)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := validateInheritedManifestBinding(capsule, header, decoded); err == nil ||
		!strings.Contains(err.Error(), "digest") {
		t.Fatalf("rewritten manifest binding error = %v, want digest refusal", err)
	}
	_ = rollbackUpgradeDescriptors(manifest)
}

func TestManifestReadDeadlineRunsArmedCleanupWithoutDescriptorABA(t *testing.T) {
	dir := t.TempDir()
	listenerR, listenerW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer listenerW.Close()
	sessionR, sessionW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer sessionW.Close()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	manifest := validUpgradeManifestForBoundaryTest(t, int(listenerR.Fd()), []UpgradeSession{{
		ID: "strath", Fd: int(sessionR.Fd()),
		ScrollbackFd: openUpgradeScrollbackFD(t, filepath.Join(dir, "strath.log")),
		PID:          cmd.Process.Pid, PIDStartTime: start,
	}})
	path, err := WriteManifest(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareManifestHandoff(path, manifest); err != nil {
		t.Fatal(err)
	}
	if err := prepareOwnershipCapsule(manifest); err != nil {
		t.Fatal(err)
	}
	owned, err := readInheritedOwnershipCapsule(manifest.ownershipCapsule)
	if err != nil {
		t.Fatal(err)
	}
	guard := newUpgradeOwnershipGuard(owned, time.Now().Add(time.Second))

	originalPread := upgradePread
	release := make(chan struct{})
	entered := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	upgradePread = func(fd int, data []byte, offset int64) (int, error) {
		once.Do(func() { close(entered) })
		<-release
		select {
		case <-finished:
		default:
			close(finished)
		}

		// A terminal error guarantees preadFull cannot re-enter the global seam
		// after the test restores it. The abandoned worker still owns and closes
		// its dedicated duplicate on return.
		return 0, errors.New("dreich manifest read released")
	}
	t.Cleanup(func() { upgradePread = originalPread })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, readErr := readInheritedManifestHandoff(ctx, strconv.Itoa(manifest.ownershipFD))
	if readErr == nil {
		t.Fatal("stalled manifest read did not hit adoption deadline")
	}
	<-entered
	if err := guard.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := listenerW.Write([]byte("braw")); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("listener ownership survived deadline cleanup: %v", err)
	}
	if _, err := sessionW.Write([]byte("canny")); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("session ownership survived deadline cleanup: %v", err)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("dedicated manifest reader did not finish after test release")
	}
}

func TestPendingUpgradeJournalColdRecoveryAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	startProcess := func() (*exec.Cmd, int64) {
		cmd := exec.Command("sleep", "30")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		start, err := grpty.ProcessStartTime(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		})

		return cmd, start
	}
	session, sessionStart := startProcess()
	helper, helperStart := startProcess()
	manifest := validUpgradeManifestForBoundaryTest(t, 7, []UpgradeSession{{
		ID: "croft", Fd: 9, PID: session.Process.Pid, PIDStartTime: sessionStart,
	}})
	manifest.Paths.RuntimeDir = canonicalUpgradePath(dir)
	manifest.Helpers = []UpgradeHelper{{PID: helper.Process.Pid, StartTime: helperStart}}
	path, err := WriteManifest(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteManifest(dir, validUpgradeManifestForBoundaryTest(t, 11, nil)); err == nil {
		t.Fatal("a second upgrade overwrote an unresolved journal")
	}
	if err := recoverPendingUpgradeJournals(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolved journal remains: %v", err)
	}
	for _, pid := range []int{session.Process.Pid, helper.Process.Pid} {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("cold recovery process %d remains: %v", pid, err)
		}
	}
}

func TestCompletedUpgradeJournalRecoveryNeverSignalsLiveProcess(t *testing.T) {
	for _, phase := range []string{upgradeJournalCommitted, upgradeJournalRolledBack} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command("sleep", "30")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			start, err := grpty.ProcessStartTime(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Wait()
			})
			manifest := validUpgradeManifestForBoundaryTest(t, 7, []UpgradeSession{{
				ID: "bairn", Fd: 9, PID: cmd.Process.Pid, PIDStartTime: start,
			}})
			manifest.Paths.RuntimeDir = canonicalUpgradePath(dir)
			path, err := WriteManifest(dir, manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeUpgradeJournalMarker(path, manifest, phase); err != nil {
				t.Fatal(err)
			}
			// Simulate a post-phase cleanup failure which leaves both durable
			// artifacts for the next ordinary daemon generation.
			journalIno := manifest.journalIno
			manifest.journalIno++
			if err := removeUpgradeJournal(path, manifest); err == nil {
				t.Fatal("replaced journal identity unexpectedly removed")
			}
			manifest.journalIno = journalIno
			if err := recoverPendingUpgradeJournals(dir); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
				t.Fatalf("completed journal recovery signaled live process: %v", err)
			}
			artifacts, err := upgradeJournalPaths(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(artifacts) != 0 {
				t.Fatalf("completed journal artifacts remain: %v", artifacts)
			}
		})
	}
}

func TestColdRecoveryValidatesGlobalArtifactIdentityBeforeSignals(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	manifest := validUpgradeManifestForBoundaryTest(t, 7, []UpgradeSession{{
		ID: "croft", Fd: 9, PID: cmd.Process.Pid, PIDStartTime: start,
	}})
	manifest.Paths.RuntimeDir = canonicalUpgradePath(dir)
	path, err := WriteManifest(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conflict := filepath.Join(dir, "upgrade-adoption-"+strings.Repeat("a", 32)+".pending")
	if err := os.WriteFile(conflict, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverPendingUpgradeJournals(dir); err == nil || !strings.Contains(err.Error(), "multiple pending") {
		t.Fatalf("conflicting artifact recovery error = %v", err)
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("global artifact conflict signaled live process: %v", err)
	}
	if err := os.Remove(conflict); err != nil {
		t.Fatal(err)
	}
	misnamed := filepath.Join(dir, "upgrade-adoption-"+strings.Repeat("b", 32)+".pending")
	if err := os.Rename(path, misnamed); err != nil {
		t.Fatal(err)
	}
	if err := recoverPendingUpgradeJournals(dir); err == nil || !strings.Contains(err.Error(), "misplaced") {
		t.Fatalf("misnamed artifact recovery error = %v", err)
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("misnamed artifact recovery signaled live process: %v", err)
	}
}

func TestUpgradeJournalPathOpensRejectFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "upgrade-adoption-"+strings.Repeat("c", 32)+".pending")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	assertQuick := func(name string, fn func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- fn() }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("%s accepted FIFO", name)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s blocked opening FIFO", name)
		}
	}
	assertQuick("ReadManifest", func() error {
		_, err := ReadManifest(path)
		return err
	})
	assertQuick("prepareManifestHandoff", func() error {
		return prepareManifestHandoff(path, &UpgradeManifest{ListenerFd: 7})
	})
	manifest := validUpgradeManifestForBoundaryTest(t, 7, nil)
	manifest.JournalID = strings.Repeat("c", 32)
	manifest.journalSHA256 = sha256.Sum256([]byte("canny"))
	marker := upgradeJournalMarkerPath(path, upgradeJournalCommitted)
	if err := unix.Mkfifo(marker, 0o600); err != nil {
		t.Fatal(err)
	}
	assertQuick("readUpgradeJournalMarker", func() error {
		_, err := readUpgradeJournalMarker(path, manifest)
		return err
	})
}

func TestUpgradeJournalMarkerRejectsSymlinkAndConflictingPhases(t *testing.T) {
	dir := t.TempDir()
	manifest := validUpgradeManifestForBoundaryTest(t, 7, nil)
	path, err := WriteManifest(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "dreich-marker")
	if err := os.WriteFile(target, []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, upgradeJournalMarkerPath(path, upgradeJournalCommitted)); err != nil {
		t.Fatal(err)
	}
	if _, err := readUpgradeJournalMarker(path, manifest); err == nil {
		t.Fatal("symlink marker was accepted")
	}
	if err := os.Remove(upgradeJournalMarkerPath(path, upgradeJournalCommitted)); err != nil {
		t.Fatal(err)
	}
	if err := writeUpgradeJournalMarker(path, manifest, upgradeJournalCommitted); err != nil {
		t.Fatal(err)
	}
	if err := writeUpgradeJournalMarker(path, manifest, upgradeJournalRolledBack); err != nil {
		t.Fatal(err)
	}
	if _, err := readUpgradeJournalMarker(path, manifest); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting journal markers error = %v", err)
	}
}

func validUpgradeManifestForBoundaryTest(t *testing.T, listenerFD int, sessions []UpgradeSession) *UpgradeManifest {
	t.Helper()
	sessions = slices.Clone(sessions)
	usedFDs := map[int]struct{}{listenerFD: {}}
	for _, session := range sessions {
		usedFDs[session.Fd] = struct{}{}
		if session.ScrollbackFd > 2 {
			usedFDs[session.ScrollbackFd] = struct{}{}
		}
	}
	nextFD := 100_000
	for i := range sessions {
		if sessions[i].ScrollbackFd <= 2 {
			for {
				if _, exists := usedFDs[nextFD]; !exists {
					break
				}
				nextFD++
			}
			sessions[i].ScrollbackFd = nextFD
			usedFDs[nextFD] = struct{}{}
			nextFD++
		}
		if sessions[i].PIDStartTime <= 0 {
			sessions[i].PIDStartTime = int64(i + 1)
		}
	}
	return &UpgradeManifest{
		Version: upgradeManifestVersion, ListenerFd: listenerFD, Sessions: sessions,
		StateSnapshot: []byte(`{"version":23,"sessions":{}}`), ConfigFile: "/braw/config",
		Target: UpgradeTargetDescriptor{ResolvedPath: "/braw/gr", SHA256: strings.Repeat("a", 64)},
		Paths: UpgradePathDescriptor{
			ConfigFile: "/braw/config", DataDir: "/braw/data", StateFile: "/braw/state",
			RuntimeDir: "/braw/run", SocketPath: "/braw/run/socket",
		},
	}
}

func openUpgradeScrollbackFD(t *testing.T, path string) int {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	if err := setDescriptorFlags(fd, syscall.FD_CLOEXEC); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	return fd
}

func writeCapacityProbeExecutable(t *testing.T, probe any) string {
	t.Helper()
	data, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gr-probe")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\n", data)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	return path
}

func compatibleCapacityProbe(backend string) UpgradeCapacityProbe {
	return UpgradeCapacityProbe{
		Version:              upgradeCapacityProbeVersion,
		Backend:              backend,
		HelperHandoffVersion: upgradeHelperHandoffVersion,
		StateVersion:         CurrentStateVersion,
		ManifestVersion:      upgradeManifestVersion,
		AdoptionVersion:      upgradeManifestVersion,
	}
}

func TestProbeUpgradeTargetContracts(t *testing.T) {
	tests := []struct {
		name    string
		probe   UpgradeCapacityProbe
		wantCap int
		wantErr bool
	}{
		{
			name: "unlimited", probe: compatibleCapacityProbe("unlimited"),
		},
		{
			name: "limited", probe: func() UpgradeCapacityProbe {
				probe := compatibleCapacityProbe("limited")
				probe.MaxSessions = 64
				return probe
			}(), wantCap: 64,
		},
		{
			name: "unavailable", probe: compatibleCapacityProbe("unavailable"), wantErr: true,
		},
		{
			name: "unknown version", probe: func() UpgradeCapacityProbe {
				probe := compatibleCapacityProbe("unlimited")
				probe.Version = 99
				return probe
			}(), wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := probeUpgradeTarget(writeCapacityProbeExecutable(t, tt.probe))
			if (err != nil) != tt.wantErr {
				t.Fatalf("probeUpgradeTarget error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && target.capacity != tt.wantCap {
				t.Errorf("capacity = %d, want %d", target.capacity, tt.wantCap)
			}
			if target != nil {
				t.Cleanup(func() { _ = target.pin.close() })
			}
		})
	}
}

func TestUpgradeCapacityProbeFiltersEnvironment(t *testing.T) {
	const sentinel = "GRAITH_TEST_SENTINEL_SECRET"
	t.Setenv(sentinel, "never-forward-this-value")

	for _, profile := range []string{"", "canny"} {
		t.Run(map[bool]string{true: "default", false: "named"}[profile == ""], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gr-probe")
			script := fmt.Sprintf("#!/bin/sh\nif [ -n \"${%s+x}\" ]; then exit 42; fi\nprintf '{}'\n", sentinel)
			if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			pin, err := pinUpgradeTarget(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pin.close() })
			if _, err := runUpgradeCapacityProbe(pin, "", profile); err != nil {
				t.Fatalf("probe inherited sensitive environment: %v", err)
			}
		})
	}
}

func TestProbeUpgradeTargetExplicitInvalidDoesNotFallback(t *testing.T) {
	_, err := probeUpgradeTarget(filepath.Join(t.TempDir(), "missing-gr"))
	var refusal *upgradeRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want upgrade refusal", err)
	}
}

func TestProbeUpgradeTargetBoundsOutputAndTime(t *testing.T) {
	tests := []struct {
		name   string
		script string
	}{
		{"oversized", "#!/bin/sh\nhead -c 1025 /dev/zero\n"},
		{"timeout", "#!/bin/sh\nsleep 3\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gr-probe")
			if err := os.WriteFile(path, []byte(tt.script), 0o700); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if _, err := probeUpgradeTarget(path); err == nil {
				t.Fatal("probeUpgradeTarget succeeded")
			}
			if time.Since(started) > 4*time.Second {
				t.Fatal("capacity probe was not bounded")
			}
		})
	}
}

func TestUpgradeTargetUsesImmutablePinnedCopy(t *testing.T) {
	path := writeCapacityProbeExecutable(t, compatibleCapacityProbe("unlimited"))
	target, err := probeUpgradeTarget(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.pin.close() })
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := bytes.Clone(original)
	replacement[len(replacement)-2] ^= 1
	if err := os.WriteFile(path, replacement, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := target.validateFileIdentity(); err != nil {
		t.Fatalf("original path mutation changed the pinned target: %v", err)
	}
	data, err := runUpgradeCapacityProbe(target.pin, "", "")
	if err != nil {
		t.Fatalf("pinned target no longer executes after source mutation: %v", err)
	}
	var probe UpgradeCapacityProbe
	if err := json.Unmarshal(data, &probe); err != nil || probe != compatibleCapacityProbe("unlimited") {
		t.Fatalf("pinned probe = %+v, err = %v", probe, err)
	}
}

func TestUpgradeOwnershipGuardCleansEveryTransferredResource(t *testing.T) {
	listenerR, listenerW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	listenerFD, err := syscall.Dup(int(listenerR.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := setDescriptorFlags(listenerFD, syscall.FD_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	listenerR.Close()
	defer listenerW.Close()
	sessionR, sessionW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	sessionFD, err := syscall.Dup(int(sessionR.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := setDescriptorFlags(sessionFD, syscall.FD_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	sessionR.Close()
	defer sessionW.Close()

	startChild := func() (int, int64) {
		t.Helper()
		cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30 & wait")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		start, err := grpty.ProcessStartTime(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}

		return cmd.Process.Pid, start
	}
	agentPID, agentStart := startChild()
	helperPID, helperStart := startChild()
	manifest := &UpgradeManifest{
		ListenerFd: listenerFD,
		Sessions: []UpgradeSession{{
			ID: "canny", Fd: sessionFD, PID: agentPID, PIDStartTime: agentStart,
		}},
		Helpers: []UpgradeHelper{{PID: helperPID, StartTime: helperStart}},
	}
	guard := newUpgradeOwnershipGuard(manifest)
	if err := guard.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := listenerW.Write([]byte("listener ownership")); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("listener write after cleanup = %v", err)
	}
	if _, err := sessionW.Write([]byte("session ownership")); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("session write after cleanup = %v", err)
	}
	for _, pid := range []int{agentPID, helperPID} {
		var status syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
			t.Fatalf("transferred child %d remains waitable: %v", pid, err)
		}
		if !exactProcessGroupGone(pid) {
			t.Fatalf("transferred process group %d remains alive", pid)
		}
	}
}

func TestUpgradeHelperHandoffRequiresExactTargetSupport(t *testing.T) {
	helpers := []grpty.HelperProcessIdentity{{PID: 4242, StartTime: 7}}
	if err := validateUpgradeHelperHandoff(&upgradeTarget{helperHandoffVersion: 0}, helpers); err == nil {
		t.Fatal("legacy target accepted a live helper registry")
	}
	if err := validateUpgradeHelperHandoff(
		&upgradeTarget{helperHandoffVersion: upgradeHelperHandoffVersion}, helpers,
	); err != nil {
		t.Fatalf("compatible target rejected helper registry: %v", err)
	}
	if err := validateUpgradeHelperHandoff(&upgradeTarget{}, nil); err != nil {
		t.Fatalf("legacy target rejected helper-free transition: %v", err)
	}
}

func TestPrepareUpgradeDescriptorTransactionAndOrdering(t *testing.T) {
	sm := sleeperSM(t)
	session, err := grpty.NewSession(grpty.SessionOpts{
		ID: "canny", Command: "sleep", Args: []string{"30"},
		Rows: 24, Cols: 80, LogPath: filepath.Join(sm.paths.LogDir, "canny.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.ForceKill()
		<-session.Done()
		session.Close()
	})
	startTime, err := grpty.ProcessStartTime(session.ProcessPID())
	if err != nil {
		t.Fatal(err)
	}
	sm.sessions["canny"] = session
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Name: "canny", Status: StatusRunning,
		PID: session.ProcessPID(), PIDStartTime: startTime,
	}
	listenerR, listenerW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer listenerR.Close()
	defer listenerW.Close()
	for _, fd := range []int{int(listenerR.Fd()), int(session.Fd())} {
		if err := setDescriptorFlags(fd, syscall.FD_CLOEXEC); err != nil {
			t.Fatal(err)
		}
	}

	manifest, err := sm.PrepareUpgrade(listenerR.Fd(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Sessions) != 1 || manifest.Sessions[0].ID != "canny" {
		t.Fatalf("sessions = %+v", manifest.Sessions)
	}
	handoffFD := manifest.Sessions[0].Fd
	if handoffFD == int(session.Fd()) {
		t.Fatal("upgrade manifest retained the session-owned descriptor")
	}
	if flags, err := descriptorFlags(handoffFD); err != nil || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("planned handoff descriptor was not CLOEXEC: flags = %d, err = %v", flags, err)
	}
	if flags, err := descriptorFlags(int(session.Fd())); err != nil || flags != syscall.FD_CLOEXEC {
		t.Fatalf("original PTY descriptor flags = %d, err = %v", flags, err)
	}
	if err := makeUpgradeDescriptorsInheritable(manifest); err != nil {
		t.Fatal(err)
	}
	if flags, err := descriptorFlags(handoffFD); err != nil || flags&syscall.FD_CLOEXEC != 0 {
		t.Fatalf("final handoff descriptor flags = %d, err = %v", flags, err)
	}
	if err := rollbackUpgradeDescriptors(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := descriptorFlags(handoffFD); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("owned handoff descriptor remains open: %v", err)
	}
	for _, fd := range []int{int(listenerR.Fd()), int(session.Fd())} {
		flags, err := descriptorFlags(fd)
		if err != nil {
			t.Fatal(err)
		}
		if flags != syscall.FD_CLOEXEC {
			t.Errorf("descriptor %d flags = %d, want exact CLOEXEC restore", fd, flags)
		}
	}
}

func TestRollbackUpgradeDescriptorsTreatsClosedOriginalAsResolved(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	fd := int(r.Fd())
	manifest := &UpgradeManifest{descriptorFlags: map[int]int{fd: syscall.FD_CLOEXEC}}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rollbackUpgradeDescriptors(manifest); err != nil {
		t.Fatalf("closed descriptor rollback error = %v", err)
	}
	if manifest.descriptorFlags != nil {
		t.Fatalf("closed descriptor bookkeeping was retained: %+v", manifest.descriptorFlags)
	}
}

func TestRollbackUpgradeDescriptorsRetainsUnsafeRestorationForRetry(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := int(r.Fd())
	if err := setDescriptorFlags(fd, 0); err != nil {
		t.Fatal(err)
	}
	manifest := &UpgradeManifest{descriptorFlags: map[int]int{fd: syscall.FD_CLOEXEC}}

	originalSet := rollbackSetDescriptorFlags
	rollbackSetDescriptorFlags = func(int, int) error { return syscall.EIO }
	t.Cleanup(func() { rollbackSetDescriptorFlags = originalSet })
	err = rollbackUpgradeDescriptors(manifest)
	var unsafeErr *upgradeDescriptorSafetyError
	if !errors.As(err, &unsafeErr) {
		t.Fatalf("rollback error = %v, want unsafe descriptor error", err)
	}
	if _, ok := manifest.descriptorFlags[fd]; !ok {
		t.Fatal("failed restoration bookkeeping was discarded")
	}

	rollbackSetDescriptorFlags = originalSet
	if err := rollbackUpgradeDescriptors(manifest); err != nil {
		t.Fatalf("retry rollback error = %v", err)
	}
	flags, err := descriptorFlags(fd)
	if err != nil || flags != syscall.FD_CLOEXEC {
		t.Fatalf("retry flags = %d, err = %v", flags, err)
	}
}

func TestUnsafeRollbackForkLockPreventsListenerAndPTMInheritance(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenerFile, err := listener.(*net.TCPListener).File()
	_ = listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	listenerFD, err := unix.FcntlInt(listenerFile.Fd(), unix.F_DUPFD_CLOEXEC, 100)
	_ = listenerFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(listenerFD)

	sm := sleeperSM(t)
	session, err := grpty.NewSession(grpty.SessionOpts{
		ID: "thrawn-forklock", Command: "sleep", Args: []string{"30"}, Rows: 24, Cols: 80,
		LogPath: filepath.Join(sm.paths.LogDir, "thrawn-forklock.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.ForceKill()
		<-session.Done()
		session.Close()
	})
	ptmFD, err := unix.FcntlInt(session.Fd(), unix.F_DUPFD_CLOEXEC, listenerFD+1)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(ptmFD)
	manifest := &UpgradeManifest{
		descriptorFlags: map[int]int{
			listenerFD: syscall.FD_CLOEXEC,
			ptmFD:      syscall.FD_CLOEXEC,
		},
		ownedDescriptors: map[int]struct{}{listenerFD: {}, ptmFD: {}},
	}

	syscall.ForkLock.Lock()
	forkLocked := true
	defer func() {
		if forkLocked {
			syscall.ForkLock.Unlock()
		}
	}()
	if err := makeUpgradeDescriptorsInheritable(manifest); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sh", "-c", `for fd in "$@"; do if [ -e "/dev/fd/$fd" ] || [ -e "/proc/self/fd/$fd" ]; then exit 42; fi; done`,
		"sh", strconv.Itoa(listenerFD), strconv.Itoa(ptmFD))
	childDone := make(chan error, 1)
	go func() { childDone <- child.Run() }()
	select {
	case err := <-childDone:
		t.Fatalf("queued child crossed ForkLock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	originalClose := rollbackCloseDescriptor
	rollbackCloseDescriptor = func(int) error { return syscall.EIO }
	rollbackErr := rollbackUpgradeDescriptors(manifest)
	rollbackCloseDescriptor = originalClose
	if rollbackErr == nil {
		t.Fatal("injected rollback close failure was ignored")
	}
	for _, fd := range []int{listenerFD, ptmFD} {
		flags, err := descriptorFlags(fd)
		if err != nil || flags&syscall.FD_CLOEXEC == 0 {
			t.Fatalf("descriptor %d was not secured before ForkLock release: flags=%d err=%v", fd, flags, err)
		}
	}

	signals := make(chan os.Signal)
	upgrades := make(chan *upgradeRequest)
	shutdown := make(chan struct{}, 1)
	controlDone := make(chan error, 1)
	go func() {
		controlDone <- runControlLoop(signals, upgrades, discardLogger(), func() error { return nil }, func() {
			shutdown <- struct{}{}
		}, func(*upgradeRequest) error {
			return unsafeUpgradeDescriptor(rollbackErr)
		})
	}()
	upgrades <- newUpgradeRequest("/thrawn/gr")
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("unsafe rollback did not enter fail-closed shutdown")
	}
	syscall.ForkLock.Unlock()
	forkLocked = false
	if err := <-childDone; err != nil {
		t.Fatalf("queued child observed listener/PTM handoff descriptors: %v", err)
	}
	var unsafeErr *upgradeDescriptorSafetyError
	if err := <-controlDone; !errors.As(err, &unsafeErr) {
		t.Fatalf("control loop error = %v, want unsafe rollback", err)
	}
}

func TestRollbackUpgradeDescriptorsSecuresAmbiguousOwnedClose(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd, err := syscall.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := setDescriptorFlags(fd, 0); err != nil {
		t.Fatal(err)
	}
	manifest := &UpgradeManifest{
		descriptorFlags:  map[int]int{fd: syscall.FD_CLOEXEC},
		ownedDescriptors: map[int]struct{}{fd: {}},
	}

	originalClose := rollbackCloseDescriptor
	closeCalls := 0
	rollbackCloseDescriptor = func(int) error {
		closeCalls++
		return syscall.EINTR
	}
	t.Cleanup(func() { rollbackCloseDescriptor = originalClose })
	if err := rollbackUpgradeDescriptors(manifest); !errors.Is(err, syscall.EINTR) {
		t.Fatalf("rollback error = %v, want EINTR", err)
	}
	flags, err := descriptorFlags(fd)
	if err != nil || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("ambiguous close descriptor was not secured: flags=%d err=%v", flags, err)
	}
	if manifest.descriptorFlags != nil || manifest.ownedDescriptors != nil {
		t.Fatalf("secured ambiguous close retained unsafe retry bookkeeping: flags=%+v owned=%+v", manifest.descriptorFlags, manifest.ownedDescriptors)
	}
	if err := rollbackUpgradeDescriptors(manifest); err != nil {
		t.Fatalf("idempotent rollback error = %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("ambiguous descriptor was closed %d times, want 1", closeCalls)
	}
}

func TestRollbackUpgradeDescriptorsRetainsOwnedGetFlagsFailure(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd, err := syscall.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	manifest := &UpgradeManifest{
		descriptorFlags:  map[int]int{fd: syscall.FD_CLOEXEC},
		ownedDescriptors: map[int]struct{}{fd: {}},
	}

	originalGet := rollbackGetDescriptorFlags
	rollbackGetDescriptorFlags = func(int) (int, error) { return 0, syscall.EIO }
	t.Cleanup(func() { rollbackGetDescriptorFlags = originalGet })
	err = rollbackUpgradeDescriptors(manifest)
	var unsafeErr *upgradeDescriptorSafetyError
	if !errors.As(err, &unsafeErr) {
		t.Fatalf("rollback error = %v, want unsafe descriptor error", err)
	}
	if _, ok := manifest.ownedDescriptors[fd]; !ok {
		t.Fatal("owned descriptor bookkeeping was discarded")
	}

	rollbackGetDescriptorFlags = originalGet
	if err := rollbackUpgradeDescriptors(manifest); err != nil {
		t.Fatalf("retry rollback error = %v", err)
	}
	if _, err := descriptorFlags(fd); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("retry did not close owned descriptor: %v", err)
	}
}

func TestPrepareUpgradePartialFailureClosesOwnedDuplicates(t *testing.T) {
	sm := sleeperSM(t)
	valid, err := grpty.NewSession(grpty.SessionOpts{
		ID: "canny", Command: "sleep", Args: []string{"30"}, Rows: 24, Cols: 80,
		LogPath: filepath.Join(sm.paths.LogDir, "canny-partial.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = valid.ForceKill()
		<-valid.Done()
		valid.Close()
	})
	validStart, err := grpty.ProcessStartTime(valid.ProcessPID())
	if err != nil {
		t.Fatal(err)
	}

	invalidCmd := exec.Command("sleep", "30")
	if err := invalidCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = invalidCmd.Process.Kill()
		_ = invalidCmd.Wait()
	})
	invalidStart, err := grpty.ProcessStartTime(invalidCmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	closedR, closedW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	closedW.Close()
	closedR.Close()
	invalid := &grpty.Session{ID: "dreich", Cmd: invalidCmd, Ptmx: closedR}

	sm.sessions["canny"] = valid
	sm.sessions["dreich"] = invalid
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Status: StatusRunning, PID: valid.ProcessPID(), PIDStartTime: validStart,
	}
	sm.state.Sessions["dreich"] = &SessionState{
		ID: "dreich", Status: StatusRunning, PID: invalidCmd.Process.Pid, PIDStartTime: invalidStart,
	}
	listenerR, listenerW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer listenerR.Close()
	defer listenerW.Close()
	before := countOpenDescriptors(t)
	if _, err := sm.PrepareUpgrade(listenerR.Fd(), ""); err == nil {
		t.Fatal("partial descriptor preparation succeeded")
	}
	after := countOpenDescriptors(t)
	if after != before {
		t.Fatalf("open descriptor count grew after partial prepare: before=%d after=%d", before, after)
	}
}

func countOpenDescriptors(t *testing.T) int {
	t.Helper()
	count := 0
	for fd := 0; fd < 4096; fd++ {
		if _, err := descriptorFlags(fd); err == nil {
			count++
		}
	}

	return count
}

func TestUpgradeCapacityIgnoresExitedPTY(t *testing.T) {
	sm := sleeperSM(t)
	newSession := func(id, command string, args ...string) *grpty.Session {
		t.Helper()
		session, err := grpty.NewSession(grpty.SessionOpts{
			ID: id, Command: command, Args: args, Rows: 24, Cols: 80,
			LogPath: filepath.Join(sm.paths.LogDir, id+".log"),
		})
		if err != nil {
			t.Fatal(err)
		}

		return session
	}
	exited := newSession("auld", "true")
	<-exited.Done()
	live := newSession("braw", "sleep", "30")
	t.Cleanup(func() {
		exited.Close()
		_ = live.ForceKill()
		<-live.Done()
		live.Close()
	})
	liveStart, err := grpty.ProcessStartTime(live.ProcessPID())
	if err != nil {
		t.Fatal(err)
	}
	sm.sessions["auld"] = exited
	sm.sessions["braw"] = live
	sm.state.Sessions["auld"] = &SessionState{ID: "auld", Status: StatusStopped}
	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Status: StatusRunning, PID: live.ProcessPID(), PIDStartTime: liveStart,
	}
	if err := sm.beginUpgradeReservation(); err != nil {
		t.Fatal(err)
	}
	defer sm.endUpgradeReservation()
	if err := sm.preflightUpgradeSessions(1); err != nil {
		t.Fatalf("one live plus one exited PTY exceeded live capacity: %v", err)
	}
}

func TestPersistFrozenUpgradeStateRefusesConcurrentMutation(t *testing.T) {
	sm := sleeperSM(t)
	sm.upgradePending = true
	sm.state.Sessions["canny"] = &SessionState{ID: "canny", Name: "canny", Status: StatusStopped}
	mutated := false
	sm.saveStateFault = func() error {
		if mutated {
			return nil
		}
		mutated = true
		sm.mu.Lock()
		sm.state.Sessions["canny"].Name = "dreich"
		sm.mu.Unlock()

		return nil
	}
	if err := sm.persistFrozenUpgradeState(&UpgradeManifest{planSessions: map[string]*grpty.Session{}}); err == nil {
		t.Fatal("upgrade accepted a stale persisted state snapshot")
	}
	data, err := os.ReadFile(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"name": "dreich"`)) {
		t.Fatal("refused upgrade did not restore the current state snapshot")
	}
}

func TestPersistFrozenUpgradeStateFailurePrecedesDescriptorMutation(t *testing.T) {
	sm := sleeperSM(t)
	sm.upgradePending = true
	sm.saveStateFault = func() error { return errors.New("injected state failure") }
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := setDescriptorFlags(int(r.Fd()), syscall.FD_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	if err := sm.persistFrozenUpgradeState(&UpgradeManifest{planSessions: map[string]*grpty.Session{}}); err == nil {
		t.Fatal("state persistence failure was ignored")
	}
	flags, err := descriptorFlags(int(r.Fd()))
	if err != nil || flags != syscall.FD_CLOEXEC {
		t.Fatalf("descriptor flags changed before persisted state: flags=%d err=%v", flags, err)
	}
}

func TestPersistFrozenUpgradeStateRefusesExitedPlannedSession(t *testing.T) {
	sm := sleeperSM(t)
	session, err := grpty.NewSession(grpty.SessionOpts{
		ID: "canny", Command: "sleep", Args: []string{"30"}, Rows: 24, Cols: 80,
		LogPath: filepath.Join(sm.paths.LogDir, "canny-exit-plan.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	start, err := grpty.ProcessStartTime(session.ProcessPID())
	if err != nil {
		t.Fatal(err)
	}
	sm.sessions["canny"] = session
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Status: StatusRunning, PID: session.ProcessPID(), PIDStartTime: start,
	}
	sm.upgradePending = true
	listenerR, listenerW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer listenerR.Close()
	defer listenerW.Close()
	manifest, err := sm.prepareUpgrade(listenerR.Fd(), "", 0, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rollbackUpgradeDescriptors(manifest) }()
	_ = session.ForceKill()
	<-session.Done()
	defer session.Close()
	if err := sm.persistFrozenUpgradeState(manifest); err == nil {
		t.Fatal("persisted a running snapshot after its planned process exited")
	}
}

func TestTerminateFailedUpgradeSessionKillsEntireProcessGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	startTime, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := terminateFailedUpgradeSession(UpgradeSession{
		ID: "thrawn", PID: cmd.Process.Pid, PIDStartTime: startTime,
	})
	if err != nil || !cleaned {
		t.Fatalf("terminateFailedUpgradeSession = (%v, %v)", cleaned, err)
	}
	if !exactProcessGroupGone(cmd.Process.Pid) {
		t.Fatal("failed adoption left a process group alive")
	}
}

func TestTerminateFailedUpgradeSessionDoesNotSignalECHILDProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	startTime, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	originalWait4 := upgradePreSignalWait4
	originalSignal := upgradeSessionSignal
	upgradePreSignalWait4 = func(int, *syscall.WaitStatus, int, *syscall.Rusage) (int, error) {
		return -1, syscall.ECHILD
	}
	signalCalls := 0
	upgradeSessionSignal = func(int, syscall.Signal) error {
		signalCalls++
		return nil
	}
	t.Cleanup(func() {
		upgradePreSignalWait4 = originalWait4
		upgradeSessionSignal = originalSignal
	})

	cleaned, err := terminateFailedUpgradeSessionUntil(UpgradeSession{
		ID: "thrawn", PID: cmd.Process.Pid, PIDStartTime: startTime,
	}, time.Now().Add(100*time.Millisecond))
	if err == nil || cleaned {
		t.Fatalf("ECHILD cleanup = (%v, %v), want unresolved", cleaned, err)
	}
	if signalCalls != 0 {
		t.Fatalf("signal calls = %d, want none after ECHILD", signalCalls)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unowned same-generation process was disturbed: %v", err)
	}
}

func TestWaitForExactChildTreatsChangedIdentityAsGone(t *testing.T) {
	startTime, err := grpty.ProcessStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := waitForExactChild(os.Getpid(), startTime+1, 0)
	if err != nil || !reaped {
		t.Fatalf("waitForExactChild = (%v, %v), want already gone", reaped, err)
	}
}

func TestWaitForExactChildTreatsReapedExitedHelperAsResolved(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	startTime, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	reaped, err := waitForExactChild(pid, startTime, 0)
	if err != nil || !reaped {
		t.Fatalf("already reaped helper = (%v, %v), want resolved", reaped, err)
	}
}

func TestUpgradeOwnershipGuardRetainsHelperOnIndeterminateIdentity(t *testing.T) {
	startTime, err := grpty.ProcessStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	original := upgradeProcessStartTime
	upgradeProcessStartTime = func(pid int) (int64, error) {
		return 0, syscall.EACCES
	}
	t.Cleanup(func() { upgradeProcessStartTime = original })

	guard := newUpgradeOwnershipGuard(&UpgradeManifest{
		ListenerFd: -1,
		Helpers:    []UpgradeHelper{{PID: os.Getpid(), StartTime: startTime}},
	})
	if err := guard.reapHelpers(); err == nil {
		t.Fatal("indeterminate live helper identity was accepted")
	}
	guard.mu.Lock()
	_, retained := guard.helpers[os.Getpid()]
	guard.mu.Unlock()
	if !retained {
		t.Fatal("indeterminate live helper identity was disarmed")
	}
}

func TestLegacyUpgradeCleanupRequiresExactChildhood(t *testing.T) {
	t.Run("running child is hydrated and reaped", func(t *testing.T) {
		cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30 & wait")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		})

		cleaned, err := terminateFailedUpgradeSession(UpgradeSession{ID: "canny", PID: cmd.Process.Pid})
		if err != nil || !cleaned {
			t.Fatalf("legacy child cleanup = (%v, %v)", cleaned, err)
		}
		if !exactProcessGroupGone(cmd.Process.Pid) {
			t.Fatal("legacy child process group remains alive")
		}
	})

	t.Run("non-child remains unresolved", func(t *testing.T) {
		resolved, reaped, err := resolveUpgradeCleanupIdentity(UpgradeSession{ID: "dreich", PID: os.Getpid()})
		if err == nil || reaped || resolved.PIDStartTime != 0 {
			t.Fatalf("non-child legacy identity = (%+v, %v, %v)", resolved, reaped, err)
		}
	})
}

func TestUpgradeOwnershipGuardCleansLegacyExactChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	defer w.Close()
	guard := newUpgradeOwnershipGuard(&UpgradeManifest{
		ListenerFd: -1,
		Sessions:   []UpgradeSession{{ID: "canny", Fd: fd, PID: cmd.Process.Pid}},
	})
	if err := guard.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("bothy ownership")); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("legacy transferred descriptor remains open: %v", err)
	}
}

func TestUpgradeCleanupRegistryPromotesAndRetriesExactIdentity(t *testing.T) {
	sm := sleeperSM(t)
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Status: StatusErrored, PID: cmd.Process.Pid,
	}
	attempts := 0
	sm.upgradeCleanupTry = func(session UpgradeSession) (bool, error) {
		attempts++
		if attempts == 1 {
			return false, errors.New("temporarily wedged")
		}

		return terminateFailedUpgradeSession(session)
	}
	resolved := false
	sm.registerUpgradeCleanup(UpgradeSession{ID: "canny", PID: cmd.Process.Pid}, func() { resolved = true })

	sm.reconcileUpgradeCleanup()
	state, _ := sm.Get("canny")
	if state.PIDStartTime <= 0 || state.Status != StatusErrored {
		t.Fatalf("legacy cleanup identity was not durably promoted: %+v", state)
	}
	if resolved || len(sm.upgradeCleanup) != 1 {
		t.Fatalf("wedged cleanup ownership = (resolved=%v, entries=%d)", resolved, len(sm.upgradeCleanup))
	}
	data, err := os.ReadFile(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	var persisted State
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if got := persisted.Sessions["canny"]; got == nil || got.PIDStartTime != state.PIDStartTime {
		t.Fatalf("promoted cleanup identity was not persisted: %+v", got)
	}

	sm.reconcileUpgradeCleanup()
	state, _ = sm.Get("canny")
	if !resolved || len(sm.upgradeCleanup) != 0 || state.PID != 0 || state.PIDStartTime != 0 || state.Status != StatusStopped {
		t.Fatalf("retry did not resolve cleanup ownership: resolved=%v entries=%d state=%+v", resolved, len(sm.upgradeCleanup), state)
	}
}

func TestUpgradeCleanupRegistryPersistsIdentityBeforeAttempt(t *testing.T) {
	sm := sleeperSM(t)
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Status: StatusErrored, PID: cmd.Process.Pid,
	}
	attempts := 0
	sm.upgradeCleanupTry = func(UpgradeSession) (bool, error) {
		attempts++

		return false, nil
	}
	sm.saveStateFault = func() error { return errors.New("dreich disk") }
	sm.registerUpgradeCleanup(UpgradeSession{ID: "canny", PID: cmd.Process.Pid}, nil)
	sm.reconcileUpgradeCleanup()
	if attempts != 0 {
		t.Fatalf("cleanup attempted %d times before identity persistence", attempts)
	}
	for _, entry := range sm.upgradeCleanup {
		if entry.session.PIDStartTime != 0 {
			t.Fatalf("failed persistence promoted cleanup entry: %+v", entry.session)
		}
	}

	sm.saveStateFault = nil
	sm.reconcileUpgradeCleanup()
	if attempts != 1 {
		t.Fatalf("cleanup attempts after durable promotion = %d, want 1", attempts)
	}
	for _, entry := range sm.upgradeCleanup {
		if entry.session.PIDStartTime <= 0 {
			t.Fatalf("durably persisted cleanup entry was not promoted: %+v", entry.session)
		}
	}
}

func TestUpgradeCleanupRegistryPersistsResolutionBeforeDisarm(t *testing.T) {
	sm := sleeperSM(t)
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	start, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Status: StatusErrored, PID: cmd.Process.Pid, PIDStartTime: start,
	}
	resolved := false
	sm.registerUpgradeCleanup(
		UpgradeSession{ID: "canny", PID: cmd.Process.Pid, PIDStartTime: start},
		func() { resolved = true },
	)
	sm.saveStateFault = func() error { return errors.New("dreich disk") }
	sm.reconcileUpgradeCleanup()
	if resolved || len(sm.upgradeCleanup) != 1 {
		t.Fatalf("failed stopped-state persistence lost ownership: resolved=%v entries=%d", resolved, len(sm.upgradeCleanup))
	}

	sm.saveStateFault = nil
	sm.reconcileUpgradeCleanup()
	if !resolved || len(sm.upgradeCleanup) != 0 {
		t.Fatalf("durable stopped state did not disarm ownership: resolved=%v entries=%d", resolved, len(sm.upgradeCleanup))
	}
}

func TestUpgradeCleanupRegistryDoesNotClearReusedGeneration(t *testing.T) {
	sm := sleeperSM(t)
	start, err := grpty.ProcessStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Status: StatusRunning, PID: os.Getpid(), PIDStartTime: start,
	}
	called := false
	sm.upgradeCleanupTry = func(UpgradeSession) (bool, error) {
		called = true

		return true, nil
	}
	sm.registerUpgradeCleanup(UpgradeSession{ID: "canny", PID: os.Getpid()}, nil)
	sm.reconcileUpgradeCleanup()
	state, _ := sm.Get("canny")
	if called || len(sm.upgradeCleanup) != 1 || state.PIDStartTime != start || state.Status != StatusRunning {
		t.Fatalf("non-child zero-generation cleanup mutated reused state: called=%v entries=%d state=%+v", called, len(sm.upgradeCleanup), state)
	}
}

func TestPersistLatestUpgradeStateDoesNotOverwriteConcurrentSave(t *testing.T) {
	sm := sleeperSM(t)
	sm.state.Sessions["canny"] = &SessionState{ID: "canny", Name: "auld", Status: StatusStopped}
	snapshotReady := make(chan struct{})
	concurrentSaved := make(chan struct{})
	concurrentErr := make(chan error, 1)
	var hookOnce sync.Once
	sm.persistLatestStateBeforeLock = func() {
		hookOnce.Do(func() {
			close(snapshotReady)
			<-concurrentSaved
		})
	}
	go func() {
		<-snapshotReady
		sm.mu.Lock()
		sm.state.Sessions["canny"].Name = "braw"
		err := sm.saveState()
		sm.mu.Unlock()
		concurrentErr <- err
		close(concurrentSaved)
	}()

	if err := sm.persistLatestUpgradeState(); err != nil {
		t.Fatal(err)
	}
	if err := <-concurrentErr; err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Sessions["canny"].Name; got != "braw" {
		t.Fatalf("refusal recovery overwrote concurrent state save: %q", got)
	}
}

func TestFinalUpgradeBarrierSerializesStateWriter(t *testing.T) {
	sm := sleeperSM(t)
	sm.upgradePending = true
	sm.state.Sessions["canny"] = &SessionState{ID: "canny", Name: "auld", Status: StatusStopped}
	sm.mu.Lock()
	snapshot, err := sm.snapshotUpgradeStateLocked()
	sm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	manifest := &UpgradeManifest{
		planSessions:  map[string]*grpty.Session{},
		StateSnapshot: snapshot,
	}
	barrierEntered := make(chan struct{})
	releaseBarrier := make(chan struct{})
	barrierDone := make(chan error, 1)
	commitErr := errors.New("injected exec return")
	go func() {
		barrierDone <- sm.withFinalUpgradeBarrier(manifest, func() error {
			close(barrierEntered)
			<-releaseBarrier

			return commitErr
		})
	}()
	<-barrierEntered

	writerDone := make(chan error, 1)
	go func() {
		sm.mu.Lock()
		sm.state.Sessions["canny"].Name = "braw"
		err := sm.saveState()
		sm.mu.Unlock()
		writerDone <- err
	}()
	select {
	case err := <-writerDone:
		t.Fatalf("state writer crossed final exec barrier: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseBarrier)
	if err := <-barrierDone; !errors.Is(err, commitErr) {
		t.Fatalf("barrier result = %v", err)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Sessions["canny"].Name; got != "braw" {
		t.Fatalf("accepted writer was not persisted after exec rollback: %q", got)
	}
}

func TestAdoptSessionsRejectsStateIdentityAndStatusMismatches(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*SessionState)
		wantPending bool
	}{
		{"pid", func(state *SessionState) { state.PID += 100000 }, true},
		{"start", func(state *SessionState) { state.PIDStartTime++ }, true},
		{"status", func(state *SessionState) { state.Status = StatusStopped }, false},
		{"deleted", func(state *SessionState) {
			now := time.Now()
			state.DeletedAt = &now
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := sleeperSM(t)
			cmd := exec.Command("sleep", "30")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			start, err := grpty.ProcessStartTime(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			fd, err := syscall.Dup(int(r.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			r.Close()
			defer w.Close()
			marker := "canny rejected final drain " + tt.name
			if _, err := w.Write([]byte(marker)); err != nil {
				t.Fatal(err)
			}
			state := &SessionState{
				ID: "canny", Name: "canny", Status: StatusRunning,
				PID: cmd.Process.Pid, PIDStartTime: start,
			}
			tt.mutate(state)
			sm.state.Sessions["canny"] = state
			scrollbackFD := openUpgradeScrollbackFD(t, filepath.Join(sm.paths.LogDir, "canny.log"))
			result, err := sm.AdoptSessions(&UpgradeManifest{Sessions: []UpgradeSession{{
				ID: "canny", Fd: fd, ScrollbackFd: scrollbackFD,
				PID: cmd.Process.Pid, PIDStartTime: start,
			}}})
			if err != nil {
				t.Fatalf("AdoptSessions error = %v", err)
			}
			if got := len(result.UnresolvedSessions) > 0; got != tt.wantPending {
				t.Fatalf("pending cleanup = %v, want %v", got, tt.wantPending)
			}
			for _, unresolved := range result.UnresolvedSessions {
				sm.registerUpgradeCleanup(unresolved, nil)
			}
			sm.reconcileUpgradeCleanup()
			if !slices.ContainsFunc(result.ResolvedSessions, func(session UpgradeSession) bool { return session.ID == "canny" }) {
				t.Fatal("rejected manifest process ownership was not resolved")
			}
			if _, ok := sm.GetPTY("canny"); ok {
				t.Fatal("mismatched session was adopted")
			}
			if _, err := w.Write([]byte("ownership")); !errors.Is(err, syscall.EPIPE) {
				t.Fatalf("transferred descriptor remains open: %v", err)
			}
			logData, err := os.ReadFile(filepath.Join(sm.paths.LogDir, "canny.log"))
			if err != nil || !bytes.Contains(logData, []byte(marker)) {
				t.Fatalf("rejected PTY final drain = %q, err=%v", logData, err)
			}
			got, _ := sm.Get("canny")
			if got.PID != 0 {
				t.Fatal("mismatched durable identity was not reconciled")
			}
			_ = cmd.Wait()
		})
	}
}

func TestAdoptSessionsPreflightRejectionDrainsEveryValidPTY(t *testing.T) {
	sm := sleeperSM(t)
	manifest := &UpgradeManifest{}
	type drainFixture struct {
		path   string
		marker string
		writer *os.File
	}
	fixtures := make([]drainFixture, 0, 2)
	for _, id := range []string{"canny", "dreich"} {
		readEnd, writeEnd, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Dup(int(readEnd.Fd()))
		_ = readEnd.Close()
		if err != nil {
			t.Fatal(err)
		}
		marker := id + " preflight final drain"
		if _, err := writeEnd.Write([]byte(marker)); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(sm.paths.LogDir, id+".log")
		manifest.Sessions = append(manifest.Sessions, UpgradeSession{
			ID: id, Fd: fd, ScrollbackFd: openUpgradeScrollbackFD(t, path),
			PID: 1 << 29, PIDStartTime: 1,
		})
		fixtures = append(fixtures, drainFixture{path: path, marker: marker, writer: writeEnd})
	}
	manifest.Sessions = append(manifest.Sessions, UpgradeSession{
		ID: "thrawn", Fd: -1,
		ScrollbackFd: openUpgradeScrollbackFD(t, filepath.Join(sm.paths.LogDir, "thrawn.log")),
		PID:          1 << 29, PIDStartTime: 2,
	})

	if _, err := sm.AdoptSessions(manifest); err == nil {
		t.Fatal("invalid descriptor preflight was accepted")
	}
	for _, fixture := range fixtures {
		if _, err := fixture.writer.Write([]byte("ownership")); !errors.Is(err, syscall.EPIPE) {
			t.Errorf("%s descriptor remains open: %v", fixture.marker, err)
		}
		_ = fixture.writer.Close()
		data, err := os.ReadFile(fixture.path)
		if err != nil || !bytes.Contains(data, []byte(fixture.marker)) {
			t.Errorf("%s scrollback = %q, err=%v", fixture.marker, data, err)
		}
	}
}

func TestPostListenerConversionFailureDrainsAndReapsTransferredSession(t *testing.T) {
	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenerFile, err := baseListener.(*net.TCPListener).File()
	_ = baseListener.Close()
	if err != nil {
		t.Fatal(err)
	}
	listenerFD, err := syscall.Dup(int(listenerFile.Fd()))
	_ = listenerFile.Close()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	startTime, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(readEnd.Fd()))
	_ = readEnd.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer writeEnd.Close()
	marker := []byte("canny post-listener final drain")
	if _, err := writeEnd.Write(marker); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "canny.log")
	manifest := &UpgradeManifest{
		ListenerFd: listenerFD,
		Sessions: []UpgradeSession{{
			ID: "canny", Fd: fd, ScrollbackFd: openUpgradeScrollbackFD(t, logPath),
			PID: cmd.Process.Pid, PIDStartTime: startTime,
		}},
	}
	guard := newUpgradeOwnershipGuard(manifest, time.Now().Add(5*time.Second))
	wantErr := errors.New("injected post-listener conversion failure")
	listener, err := adoptUpgradeListener(manifest, guard, func() error { return wantErr })
	if listener != nil || !errors.Is(err, wantErr) {
		t.Fatalf("listener adoption = (%v, %v), want injected failure", listener, err)
	}
	if err := guard.Cleanup(); err != nil {
		t.Fatalf("post-conversion ownership cleanup: %v", err)
	}
	if _, err := writeEnd.Write([]byte("ownership")); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("transferred PTY remained open: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || !bytes.Contains(data, marker) {
		t.Fatalf("post-conversion final drain = %q, err=%v", data, err)
	}
	if !exactProcessGroupGone(cmd.Process.Pid) {
		t.Fatal("post-conversion failure left the transferred process group alive")
	}
}

func TestAdoptSessionsUsesOneDeadlineAndDegradesLaterScreens(t *testing.T) {
	sm := sleeperSM(t)
	sm.adoptionScreenBudget = 25 * time.Millisecond
	firstReaderActive := make(chan struct{})
	releaseFirst := make(chan struct{})
	var seamMu sync.Mutex
	var degraded []bool
	sm.adoptSession = func(opts grpty.AdoptOpts) (*grpty.Session, error) {
		session, err := grpty.AdoptSession(opts)
		seamMu.Lock()
		degraded = append(degraded, opts.DegradedScreen)
		call := len(degraded)
		seamMu.Unlock()
		if call == 1 && err == nil {
			close(firstReaderActive)
			<-releaseFirst
		}

		return session, err
	}

	type fixture struct {
		id      string
		path    string
		marker  []byte
		writer  *os.File
		command *exec.Cmd
	}
	fixtures := make([]fixture, 0, 3)
	manifest := &UpgradeManifest{adoptionDeadline: time.Now().Add(6 * time.Second)}
	for _, id := range []string{"canny", "dreich", "thrawn"} {
		cmd := exec.Command("sleep", "30")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		startTime, err := grpty.ProcessStartTime(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		readEnd, writeEnd, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Dup(int(readEnd.Fd()))
		_ = readEnd.Close()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(sm.paths.LogDir, id+".log")
		marker := []byte(id + " absolute adoption deadline marker")
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: id, Status: StatusRunning,
			PID: cmd.Process.Pid, PIDStartTime: startTime,
		}
		manifest.Sessions = append(manifest.Sessions, UpgradeSession{
			ID: id, Fd: fd, ScrollbackFd: openUpgradeScrollbackFD(t, path),
			PID: cmd.Process.Pid, PIDStartTime: startTime,
		})
		fixtures = append(fixtures, fixture{
			id: id, path: path, marker: marker, writer: writeEnd, command: cmd,
		})
	}
	t.Cleanup(func() {
		for _, item := range fixtures {
			_ = item.writer.Close()
			_ = syscall.Kill(-item.command.Process.Pid, syscall.SIGKILL)
			_ = item.command.Wait()
		}
	})

	type adoptResult struct {
		result UpgradeAdoptionResult
		err    error
	}
	resultCh := make(chan adoptResult, 1)
	started := time.Now()
	go func() {
		result, err := sm.adoptSessions(manifest, nil, nil, nil, false)
		resultCh <- adoptResult{result: result, err: err}
	}()
	select {
	case <-firstReaderActive:
	case <-time.After(time.Second):
		t.Fatal("first adoption did not reach the active raw reader")
	}
	if _, err := fixtures[0].writer.Write(fixtures[0].marker); err != nil {
		t.Fatal(err)
	}
	readDeadline := time.Now().Add(time.Second)
	for {
		data, err := os.ReadFile(fixtures[0].path)
		if err == nil && bytes.Contains(data, fixtures[0].marker) {
			break
		}
		if time.Now().After(readDeadline) {
			t.Fatalf("first reader did not drain while screen attempt was held: %q, err=%v", data, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	close(releaseFirst)
	for _, item := range fixtures[1:] {
		if _, err := item.writer.Write(item.marker); err != nil {
			t.Fatal(err)
		}
	}
	var adopted adoptResult
	select {
	case adopted = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("adoption multiplied the slow-screen budget across sessions")
	}
	if adopted.err != nil || len(adopted.result.UnresolvedSessions) != 0 {
		t.Fatalf("adoption result = (%+v, %v)", adopted.result, adopted.err)
	}
	if time.Now().After(manifest.adoptionDeadline) {
		t.Fatalf("adoption exceeded its absolute deadline after %v", time.Since(started))
	}
	seamMu.Lock()
	gotDegraded := slices.Clone(degraded)
	seamMu.Unlock()
	if !slices.Equal(gotDegraded, []bool{false, true, true}) {
		t.Fatalf("degraded screen attempts = %v, want [false true true]", gotDegraded)
	}

	for _, item := range fixtures {
		_ = item.writer.Close()
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	sm.StopAll(shutdownCtx)
	cancelShutdown()
	for _, item := range fixtures {
		session, ok := sm.GetPTY(item.id)
		if !ok || !session.Exited() {
			t.Errorf("%s was not owned through shutdown", item.id)
			continue
		}
		session.Close()
		data, err := os.ReadFile(item.path)
		if err != nil || !bytes.Contains(data, item.marker) {
			t.Errorf("%s raw marker missing after shutdown: %q, err=%v", item.id, data, err)
		}
	}
}

func TestAdoptSessionsReturnsResolvedOwnershipBeforePersistError(t *testing.T) {
	sm := sleeperSM(t)
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	defer w.Close()
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Name: "canny", Status: StatusRunning,
		PID: cmd.Process.Pid, PIDStartTime: start,
	}
	sm.saveStateFault = func() error { return errors.New("injected post-adoption persistence failure") }
	scrollbackFD := openUpgradeScrollbackFD(t, filepath.Join(sm.paths.LogDir, "canny.log"))
	result, err := sm.AdoptSessions(&UpgradeManifest{Sessions: []UpgradeSession{{
		ID: "canny", Fd: fd, ScrollbackFd: scrollbackFD,
		PID: cmd.Process.Pid, PIDStartTime: start,
	}}})
	if err == nil {
		t.Fatal("post-adoption persistence failure was ignored")
	}
	if !slices.ContainsFunc(result.ResolvedSessions, func(session UpgradeSession) bool { return session.ID == "canny" }) {
		t.Fatal("successful adoption ownership was not returned before persistence error")
	}
	adopted, ok := sm.GetPTY("canny")
	if !ok {
		t.Fatal("successfully adopted session was discarded")
	}
	_ = adopted.ForceKill()
	_ = w.Close()
	<-adopted.Done()
	adopted.Close()
	sm.watchers.Wait()
}

func TestCleanupOrphanedProcessesRetriesErroredUpgradeOwnership(t *testing.T) {
	sm := sleeperSM(t)
	pid := spawnReapableSleeper(t)
	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Status: StatusErrored, PID: pid, PIDStartTime: start,
	}
	sm.cleanupOrphanedProcesses()
	state, _ := sm.Get("canny")
	if state.Status != StatusStopped || state.PID != 0 || state.PIDStartTime != 0 {
		t.Fatalf("errored upgrade ownership was not recovered: %+v", state)
	}
}

func TestCleanupOrphanedProcessesPersistsAlreadyDeadOwnership(t *testing.T) {
	sm := sleeperSM(t)
	sm.state.Sessions["canny"] = &SessionState{
		ID: "canny", Status: StatusErrored, PID: 1 << 30, PIDStartTime: 99,
	}
	sm.cleanupOrphanedProcesses()
	state, _ := sm.Get("canny")
	if state.Status != StatusStopped || state.PID != 0 || state.PIDStartTime != 0 {
		t.Fatalf("dead upgrade ownership was not reconciled: %+v", state)
	}
	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Sessions["canny"]; got.Status != StatusStopped || got.PID != 0 {
		t.Fatalf("dead upgrade ownership was not persisted: %+v", got)
	}
}

func TestUpgradeReservationClosesAdmissionAndDrainsInFlightCreation(t *testing.T) {
	sm := sleeperSM(t)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	sm.state.Sessions["thrawn"] = &SessionState{
		ID: "thrawn", Name: "thrawn", Status: StatusCreating, PID: cmd.Process.Pid,
	}

	if err := sm.beginUpgradeReservation(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	waitErr := sm.waitLifecycleIdle(ctx)
	cancel()
	if !errors.Is(waitErr, context.DeadlineExceeded) {
		t.Fatalf("in-flight lifecycle drain error = %v, want deadline", waitErr)
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("reservation disturbed in-flight command: %v", err)
	}
	if !sm.upgradePending {
		t.Fatal("upgrade reservation did not close later admission")
	}
	sm.mu.Lock()
	sm.state.Sessions["thrawn"].Status = StatusRunning
	sm.mu.Unlock()
	if err := sm.waitLifecycleIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	sm.endUpgradeReservation()
}

func TestUpgradeReservationRejectsLifecycleAtTrueEntry(t *testing.T) {
	sm := sleeperSM(t)
	sm.upgradePending = true
	checks := []struct {
		name string
		fn   func() error
	}{
		{name: "create", fn: func() error {
			_, err := sm.Create(CreateOpts{Name: "invalid name that would otherwise fail validation"})
			return err
		}},
		{name: "fork", fn: func() error {
			_, err := sm.ForkWithAgent("invalid fork name", "missing", "", "", 24, 80)
			return err
		}},
		{name: "resume", fn: func() error {
			_, err := sm.Resume("missing", 24, 80)
			return err
		}},
	}
	for _, check := range checks {
		err := check.fn()
		if err == nil || !strings.Contains(err.Error(), "upgrade is pending") {
			t.Errorf("%s entry error = %v, want upgrade refusal before validation/discovery", check.name, err)
		}
	}
	if sm.lifecycleInFlight != 0 {
		t.Fatalf("refused entry leaked lifecycle reservations: %d", sm.lifecycleInFlight)
	}
}

func TestShutdownBarrierRejectsLateCreateBeforeProcessStart(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "thrawn-started")
	cfg := config.Default()
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sh", Args: []string{"-c", `printf canny > "$1"; exec sleep 30`, "sh", marker},
	}
	sm := newSMWithConfig(t, cfg)
	spawnBarrier := make(chan struct{})
	releaseSpawn := make(chan struct{})
	sm.beforeLifecycleSpawn = func() {
		close(spawnBarrier)
		<-releaseSpawn
	}

	createDone := make(chan error, 1)
	go func() {
		_, err := sm.Create(CreateOpts{
			Name: "canny-late-create", AgentName: "sleeper", NoRepo: true, Rows: 24, Cols: 80,
		})
		createDone <- err
	}()
	select {
	case <-spawnBarrier:
	case <-time.After(10 * time.Second):
		t.Fatal("Create did not reach the shared pre-spawn barrier")
	}
	sm.beginShutdownBarrier()
	drainDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		drainDone <- sm.waitLifecycleIdle(ctx)
	}()
	select {
	case err := <-drainDone:
		t.Fatalf("lifecycle drain crossed a paused Create: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSpawn)
	if err := <-createDone; err == nil {
		t.Fatal("Create crossed the shutdown pre-spawn barrier")
	}
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	sm.StopAll(context.Background())
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected Create started its command: %v", err)
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.sessions) != 0 || len(sm.state.Sessions) != 0 {
		t.Fatalf("late Create published state after shutdown: drivers=%d state=%d",
			len(sm.sessions), len(sm.state.Sessions))
	}
}

func assertShutdownRejectsPausedLifecycle(
	t *testing.T,
	sm *SessionManager,
	operation func() error,
	marker string,
) {
	t.Helper()
	spawnBarrier := make(chan struct{})
	releaseSpawn := make(chan struct{})
	sm.beforeLifecycleSpawn = func() {
		close(spawnBarrier)
		<-releaseSpawn
	}
	operationDone := make(chan error, 1)
	go func() { operationDone <- operation() }()
	select {
	case <-spawnBarrier:
	case <-time.After(10 * time.Second):
		t.Fatal("lifecycle path did not reach the shared pre-spawn barrier")
	}
	sm.beginShutdownBarrier()
	drainDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		drainDone <- sm.waitLifecycleIdle(ctx)
	}()
	select {
	case err := <-drainDone:
		t.Fatalf("lifecycle drain crossed a paused launch: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSpawn)
	if err := <-operationDone; err == nil {
		t.Fatal("lifecycle operation crossed the shutdown pre-spawn barrier")
	}
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected lifecycle operation started its command: %v", err)
	}
}

func TestShutdownBarrierRejectsLateForkAndResumeBeforeProcessStart(t *testing.T) {
	t.Run("fork", func(t *testing.T) {
		sm, _ := crossAgentForkSM(t)
		marker := filepath.Join(t.TempDir(), "canny-fork-started")
		sm.cfg.Agents["bide-agent"] = config.Agent{
			Command: "sh", Args: []string{"-c", `printf canny > "$0"; exec sleep 30`, marker},
		}
		assertShutdownRejectsPausedLifecycle(t, sm, func() error {
			_, err := sm.ForkWithAgent("canny-late-fork", "src1", "bide-agent", "", 24, 80)
			return err
		}, marker)
		sm.mu.RLock()
		defer sm.mu.RUnlock()
		if len(sm.state.Sessions) != 1 || sm.state.Sessions["src1"] == nil {
			t.Fatalf("late Fork published state after shutdown: %+v", sm.state.Sessions)
		}
	})

	t.Run("resume", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "dreich-resume-started")
		cfg := config.Default()
		cfg.Sandbox.Enabled = false
		cfg.Agents["sleeper"] = config.Agent{
			Command: "sh", Args: []string{"-c", `printf dreich > "$0"; exec sleep 30`, marker},
		}
		sm := newSMWithConfig(t, cfg)
		sm.state.Sessions["dreich-resume"] = &SessionState{
			ID: "dreich-resume", Name: "dreich", Agent: "sleeper", Status: StatusStopped,
			WorktreePath: t.TempDir(),
		}
		if err := sm.saveState(); err != nil {
			t.Fatal(err)
		}
		assertShutdownRejectsPausedLifecycle(t, sm, func() error {
			_, err := sm.Resume("dreich-resume", 24, 80)
			return err
		}, marker)
		sm.mu.RLock()
		defer sm.mu.RUnlock()
		state := sm.state.Sessions["dreich-resume"]
		if len(sm.sessions) != 0 || state == nil || state.Status != StatusStopped {
			t.Fatalf("late Resume published state after shutdown: drivers=%d state=%+v", len(sm.sessions), state)
		}
	})
}

func TestLifecycleLaunchPathsUseSharedPreSpawnBarrier(t *testing.T) {
	for _, path := range []string{"session_create.go", "session_fork.go", "session_resume.go"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte("sm.lifecyclePreSpawnBarrier()")) {
			t.Fatalf("%s bypasses the shared final lifecycle launch barrier", path)
		}
		if !bytes.Contains(data, []byte("sm.rejectLaunchDuringUpgradeLocked()")) {
			t.Fatalf("%s bypasses the shared pre-publication lifecycle barrier", path)
		}
	}
}

func TestReadManifestRejectsSymlinkAndInsecureMode(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.json")
	if err := os.WriteFile(realPath, []byte(`{"listener_fd":3,"sessions":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "link.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(linkPath); err == nil {
		t.Fatal("ReadManifest followed a symlink")
	}
	if err := os.Chmod(realPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(realPath); err == nil {
		t.Fatal("ReadManifest accepted insecure permissions")
	}
}

func TestStopDaemonNonExistentPidFile(t *testing.T) {
	err := StopDaemon(filepath.Join(t.TempDir(), "nonexistent.pid"))
	if err == nil {
		t.Fatal("expected error for nonexistent pid file")
	}

	want := "daemon not running (no pid file)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestStopDaemonInvalidPID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"pid zero", "0", "refusing to signal invalid pid 0"},
		{"pid one", "1", "refusing to signal invalid pid 1"},
		{"pid negative", "-1", "refusing to signal invalid pid -1"},
		{"not a number", "notapid", "invalid pid file"},
		{"empty file", "", "invalid pid file"},
		{"trailing garbage", "123abc", "invalid pid file"},
		{"multiple numbers", "123 456", "invalid pid file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "daemon.pid")
			if err := os.WriteFile(pidFile, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			err := StopDaemon(pidFile)
			if err == nil {
				t.Fatal("expected error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}

			if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
				t.Error("expected pid file to be removed after invalid content")
			}
		})
	}
}
