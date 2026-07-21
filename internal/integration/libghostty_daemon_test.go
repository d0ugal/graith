//go:build integration && libghostty && cgo && darwin && arm64

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
	"github.com/pelletier/go-toml/v2"
)

const (
	nativeBinaryEnv      = "GRAITH_LIBGHOSTTY_DAEMON_BINARY"
	nativeSoakCyclesEnv  = "GRAITH_LIBGHOSTTY_SOAK_CYCLES"
	nativeSoakTimeoutEnv = "GRAITH_LIBGHOSTTY_SOAK_TIMEOUT"
	nativeLongSoakEnv    = "GRAITH_LIBGHOSTTY_LONG_SOAK"
	nativeProfile        = "native-validation"
	nativeReadyText      = "braw-ready"
	nativeResumeText     = "canny-resumed"
	nativeOpTimeout      = 8 * time.Second
	nativeUpgradeTimeout = protocol.UpgradeReadinessTimeout
)

const nativeDiagnosticTailBytes = 8 * 1024

type nativeDaemonHarness struct {
	t          *testing.T
	binary     string
	configFile string
	env        []string
	socket     string
	tokenFile  string
	pidFile    string
	cmd        *exec.Cmd
	done       chan struct{}
	identity   nativeProcessIdentity
	stderr     nativeLockedBuffer
	private    []string

	mu         sync.Mutex
	clients    []*nativeControlClient
	stopped    bool
	completion string
}

type nativeLockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *nativeLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *nativeLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

type nativeControlClient struct {
	conn     net.Conn
	reader   *protocol.FrameReader
	writer   *protocol.FrameWriter
	token    string
	instance string
}

func TestNativeProcessObservation(t *testing.T) {
	identity := nativeProcessIdentity{PID: 42, StartTime: 99}

	matched, err := nativeProcessIdentityMatchesStart(identity, 99, nil)
	if err != nil || !matched {
		t.Fatal("exact native process identity did not match")
	}

	matched, err = nativeProcessIdentityMatchesStart(identity, 100, nil)
	if err != nil || matched {
		t.Fatal("reused native process ID matched the original identity")
	}

	observationErr := errors.New("dreich")

	matched, err = nativeProcessIdentityMatchesStart(identity, 99, observationErr)
	if matched || !errors.Is(err, observationErr) {
		t.Fatal("native process observation error was not preserved")
	}

	matched, err = nativeProcessIdentityMatchesStart(nativeProcessIdentity{}, 0, nil)
	if matched || err == nil {
		t.Fatal("zero native process identity did not return an error")
	}

	exists, err := nativeProcessExistenceFromSignal(syscall.ESRCH)
	if exists || err != nil {
		t.Fatal("ESRCH did not confirm native process absence")
	}

	exists, err = nativeProcessExistenceFromSignal(syscall.EPERM)
	if exists || !errors.Is(err, syscall.EPERM) {
		t.Fatal("native process existence observation error was not preserved")
	}

	child := exec.Command("/bin/sleep", "30")

	child.Env = []string{}

	if err := child.Start(); err != nil {
		t.Fatal("start synthetic child process failed")
	}

	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})

	parentStart, parentExists, err := nativeProcessStartTime(os.Getpid())
	if err != nil || !parentExists {
		t.Fatal("observe test process identity failed")
	}

	children, err := nativeChildPIDs(nativeProcessIdentity{PID: os.Getpid(), StartTime: parentStart})
	if err != nil {
		t.Fatal("list synthetic child process failed")
	}

	found := false

	for _, pid := range children {
		if pid == child.Process.Pid {
			found = true
			break
		}
	}

	if !found {
		t.Fatal("synthetic child process was not listed")
	}
}

func TestDaemonFDGrowthExceeded(t *testing.T) {
	tests := []struct {
		name              string
		baseline, current int
		want              bool
	}{
		{name: "stable lower runner baseline", baseline: 22, current: 22},
		{name: "stable higher runner baseline", baseline: 25, current: 25},
		{name: "descriptor count fell", baseline: 25, current: 24},
		{name: "injected descriptor growth", baseline: 25, current: 26, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := daemonFDGrowthExceeded(test.baseline, test.current); got != test.want {
				t.Fatalf("daemonFDGrowthExceeded(%d, %d) = %v, want %v",
					test.baseline, test.current, got, test.want)
			}
		})
	}
}

func TestIsolatedNativeEnvironmentAllowlist(t *testing.T) {
	values := map[string]string{
		"PATH": "/bothy/bin", "HOME": "/croft", "TMPDIR": "/tmp/braw",
		"LANG": "en_GB.UTF-8", "LANGUAGE": "en_GB", "LC_ALL": "C.UTF-8",
		"LC_CTYPE": "UTF-8", "LC_MESSAGES": "en_GB.UTF-8", "LC_NUMERIC": "C",
		"TERM": "xterm-256color", "GORACE": "halt_on_error=1",
		"GRAITH_TOKEN": "private", "SSH_AUTH_SOCK": "/private/agent",
	}
	lookup := func(key string) (string, bool) {
		value, ok := values[key]

		return value, ok
	}

	got := isolatedNativeEnvironmentFrom(lookup, "/config-canny", "/data-thrawn", "/run-strath")

	want := []string{
		"PATH=/bothy/bin", "HOME=/croft", "TMPDIR=/tmp/braw",
		"LANG=en_GB.UTF-8", "LANGUAGE=en_GB", "LC_ALL=C.UTF-8", "LC_CTYPE=UTF-8",
		"LC_MESSAGES=en_GB.UTF-8", "LC_NUMERIC=C",
		"TERM=xterm-256color", "GORACE=halt_on_error=1",
		"GRAITH_PROFILE=" + nativeProfile,
		"XDG_CONFIG_HOME=/config-canny",
		"XDG_DATA_HOME=/data-thrawn",
		"XDG_RUNTIME_DIR=/run-strath",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("isolated environment = %q, want %q", got, want)
	}
}

func TestLibghosttyDaemonLifecycle(t *testing.T) {
	h := startNativeDaemon(t)
	defer h.cleanup()

	if helpers := h.helperProcesses(); len(helpers) != 0 {
		t.Fatalf("native daemon started with %d unexpected screen helpers", len(helpers))
	}

	client := h.connect()
	initialInstance := client.instance
	info := createNativeSession(t, client, "braw-lifecycle")
	waitForPreview(t, client, info.ID, nativeReadyText)
	waitForHelperCount(t, h, 1)

	if err := typeNativeInput(client, info.ID, "dreich-before-restart"); err != nil {
		t.Fatal("type before daemon restart failed")
	}

	waitForPreview(t, client, info.ID, "dreich-before-restart")

	snapshot := nativeSnapshot(t, client, info.ID)
	if snapshot.Cols != 80 || snapshot.Rows != 24 || !strings.Contains(snapshot.Frame, "dreich-before-restart") {
		t.Fatal("initial native snapshot did not contain the expected geometry and synthetic output")
	}

	attach := h.connect()
	mustControl(t, attach, "attach", protocol.AttachMsg{SessionID: info.ID}, "attached")

	if err := attach.send("resize", protocol.ResizeMsg{Cols: 96, Rows: 28}); err != nil {
		t.Fatal("send native resize failed")
	}

	waitForSnapshotSize(t, client, info.ID, 96, 28)
	mustControl(t, attach, "detach", struct{}{}, "detached")
	attach.close()

	oldHelper := h.helperProcesses()[0]

	upgradeMsg := protocol.UpgradeMsg{ExecPath: h.binary, ClientVersion: version.Version}
	if upgradeMsg.ClientVersion == "" {
		t.Fatal("tagged binary version is empty")
	}

	mustControl(t, client, "upgrade_preflight", upgradeMsg, "upgrade_preflight_ok")

	if err := client.send("upgrade", upgradeMsg); err != nil {
		t.Fatal("request preserving daemon restart failed")
	}

	upgradeResponse, err := client.readControl()
	if err != nil {
		t.Fatalf("read preserving restart response failed (%T)", err)
	}

	if upgradeResponse.Type != "upgrading" {
		t.Fatalf("daemon did not accept preserving restart: %s",
			h.redactedControlResponse(upgradeResponse))
	}

	client.close()

	client = h.connectNewGeneration(initialInstance)

	waitForProcessExit(t, oldHelper)
	waitForPreview(t, client, info.ID, "dreich-before-restart")

	if client.instance == initialInstance {
		t.Fatal("daemon restart did not present a new generation")
	}

	newHelpers := waitForHelperCount(t, h, 1)
	if newHelpers[0] == oldHelper {
		t.Fatal("adoption did not replace the pre-restart native helper")
	}

	if err := typeNativeInput(client, info.ID, "thrawn-after-restart"); err != nil {
		t.Fatal("type after daemon restart failed")
	}

	waitForPreview(t, client, info.ID, "thrawn-after-restart")

	if output := h.runCLI("--json", "list"); !json.Valid(output) {
		t.Fatal("tagged client binary did not return valid JSON from the restarted daemon")
	}

	purgeNativeSession(t, client, info.ID)
	waitForProcessExit(t, newHelpers[0])
	waitForHelperCount(t, h, 0)
	// Compare cleanup within the replacement daemon generation. Its runtime
	// baseline can legitimately differ from the process before exec, while the
	// open control connection is held constant across this observation window.
	baselineFD := daemonFDCount(t, h.identity)

	const concurrentSessions = 3

	sessions := make([]protocol.SessionInfo, 0, concurrentSessions)

	for i, name := range []string{"canny-one", "dreich-two", "croft-three"} {
		session := createNativeSession(t, client, name)
		sessions = append(sessions, session)

		marker := fmt.Sprintf("blether-before-%d", i)
		if err := typeNativeInput(client, session.ID, marker); err != nil {
			t.Fatal("type into concurrent native session failed")
		}

		waitForPreview(t, client, session.ID, marker)
	}

	helpersBeforeCrash := waitForHelperCount(t, h, concurrentSessions)

	crashedHelper := helpersBeforeCrash[0]
	if signaled, err := signalNativeProcess(crashedHelper, syscall.SIGKILL); err != nil || !signaled {
		t.Fatal("terminate one native screen helper failed")
	}

	waitForProcessExit(t, crashedHelper)

	for i, session := range sessions {
		marker := fmt.Sprintf("strath-after-%d", i)
		if err := typeNativeInput(client, session.ID, marker); err != nil {
			t.Fatal("daemon did not accept input after one helper crashed")
		}

		waitForPreview(t, client, session.ID, marker)
	}

	helpersAfterCrash := waitForHelperCount(t, h, concurrentSessions)
	if intersectionSize(helpersBeforeCrash, helpersAfterCrash) != concurrentSessions-1 {
		t.Fatal("one helper crash did not preserve the other native screen helpers")
	}

	probe := h.connect()
	if client.instance == "" || client.instance != probe.instance {
		t.Fatal("one helper crash changed or terminated the daemon generation")
	}

	probe.close()

	allHelpers := append(append([]nativeProcessIdentity(nil), helpersBeforeCrash...), helpersAfterCrash...)

	for _, session := range sessions {
		purgeNativeSession(t, client, session.ID)
	}

	waitForHelperCount(t, h, 0)

	for _, helper := range uniqueProcessIdentities(allHelpers) {
		waitForProcessExit(t, helper)
	}

	waitForNoFDGrowth(t, h.identity, baselineFD)
	client.close()

	shutdownClient := h.connect()
	shutdownSession := createNativeSession(t, shutdownClient, "strath-shutdown")
	waitForPreview(t, shutdownClient, shutdownSession.ID, nativeReadyText)
	shutdownHelper := waitForHelperCount(t, h, 1)[0]

	shutdownClient.close()

	if err := h.stop(); err != nil {
		t.Fatal("clean tagged daemon shutdown failed")
	}

	waitForProcessExit(t, shutdownHelper)

	for _, helper := range uniqueProcessIdentities(allHelpers) {
		waitForProcessExit(t, helper)
	}
}

func TestLibghosttyDaemonSoak(t *testing.T) {
	cycles := nativeSoakCycles(t)
	h := startNativeDaemon(t)

	defer h.cleanup()

	client := h.connect()
	fdBefore := daemonFDCount(t, h.identity)
	rssBefore := daemonRSSBytes(t, h.identity)
	helperBefore := len(h.helperProcesses())
	started := time.Now()
	deadline := started.Add(nativeSoakTimeout(t))
	inputBytes := 0
	helperRestarts := 0
	failures := make(map[string]int)

	for cycle := 0; cycle < cycles; cycle++ {
		if time.Now().After(deadline) {
			failures["deadline"]++
			break
		}

		name := fmt.Sprintf("bothy-%03d", cycle)

		info, stage := soakCreate(client, name)
		if stage != "" {
			failures[stage]++
			continue
		}

		finalMarker := fmt.Sprintf("strath-final-%03d", cycle)
		// Keep both the PTY echo and cat response inside the visible viewport so
		// final-marker validation measures delivery/reconstruction, not scroll-off.
		payload := strings.Repeat("braw-canny-", 16) + finalMarker
		if err := typeNativeInput(client, info.ID, payload); err != nil {
			failures["type"]++

			purgeNativeSessionBestEffort(client, info.ID)

			continue
		}

		inputBytes += len(payload)

		if !waitForPreviewResult(client, info.ID, finalMarker, nativeOpTimeout) {
			failures["final-preview"]++
		}

		if cycle%4 == 3 {
			helpers := waitForHelperCountResult(h, 1, nativeOpTimeout)
			if len(helpers) != 1 {
				failures["crash"]++
			} else {
				crashedHelper := helpers[0]

				signaled, signalErr := signalNativeProcess(crashedHelper, syscall.SIGKILL)
				if signalErr != nil || !signaled {
					failures["crash"]++
				} else {
					followup := fmt.Sprintf("croft-recovery-%03d", cycle)
					if err := typeNativeInput(client, info.ID, followup); err != nil {
						failures["recovery-type"]++
					} else if !waitForPreviewResult(client, info.ID, followup, nativeOpTimeout) {
						failures["recovery-preview"]++
					} else {
						inputBytes += len(followup)
						helperRestarts++
					}

					exited, exitErr := waitForProcessExitResult(crashedHelper, nativeOpTimeout)
					if exitErr != nil {
						failures["crashed-helper-observation"]++
					} else if !exited {
						failures["crashed-helper-reap"]++
					}
				}
			}
		}

		if _, err := requestNativeSnapshot(client, info.ID); err != nil {
			failures["snapshot"]++
		}

		if err := purgeNativeSessionResult(client, info.ID); err != nil {
			failures["purge"]++
		}

		if helpers := waitForHelperCountResult(h, 0, nativeOpTimeout); len(helpers) != 0 {
			failures["helper-reap"]++
		}
	}

	waitForHelperCountResult(h, helperBefore, nativeOpTimeout)
	waitForNoFDGrowth(t, h.identity, fdBefore)
	fdAfter := daemonFDCount(t, h.identity)
	helperAfter := len(h.helperProcesses())
	rssAfter := daemonRSSBytes(t, h.identity)
	fdGrowth := fdAfter - fdBefore

	if fdGrowth > 0 {
		failures["fd-growth"]++
	}

	helperGrowth := helperAfter - helperBefore
	if helperGrowth != 0 {
		failures["helper-growth"]++
	}

	rssGrowth := rssAfter - rssBefore
	rssGrowthLimit := int64(0)

	// RSS is a deliberately tolerant gross-growth signal, not the primary
	// leak oracle. The routine 12-cycle smoke reports it only; the explicit
	// long soak fails when end-start exceeds 128 MiB. Live helper identities
	// and daemon FDs have their own stricter baseline gates above.
	if nativeLongSoak() {
		rssGrowthLimit = 128 * 1024 * 1024
	}

	if rssGrowthLimit > 0 && rssGrowth > rssGrowthLimit {
		failures["rss-growth"]++
	}

	elapsed := time.Since(started)
	inputThroughput := float64(inputBytes) / elapsed.Seconds()

	failureCount := 0
	for _, count := range failures {
		failureCount += count
	}

	t.Logf("native daemon soak: cycles=%d failures=%d input_bytes=%d elapsed_ms=%d input_throughput_bytes_per_second=%.0f helper_restarts=%d fd_growth=%d helper_process_growth=%d rss_start_bytes=%d rss_end_bytes=%d rss_growth_bytes=%d rss_growth_limit_bytes=%d long_soak=%t",
		cycles, failureCount, inputBytes, elapsed.Milliseconds(), inputThroughput,
		helperRestarts, fdGrowth, helperGrowth, rssBefore, rssAfter, rssGrowth, rssGrowthLimit,
		nativeLongSoak())

	if failureCount > 0 {
		stages := make([]string, 0, len(failures))
		for stage := range failures {
			stages = append(stages, stage)
		}

		sort.Strings(stages)
		t.Fatalf("native daemon soak recorded %d failures in stages %s", failureCount, strings.Join(stages, ","))
	}
}

func startNativeDaemon(t *testing.T) *nativeDaemonHarness {
	t.Helper()

	binary := os.Getenv(nativeBinaryEnv)
	if binary == "" {
		t.Skip("external tagged Graith binary is not configured; run scripts/libghostty-native.sh daemon-test")
	}

	info, err := os.Stat(binary) //nolint:gosec // Exact external test binary selected by the caller.
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("configured tagged Graith binary is unavailable")
	}

	// Keep the isolated runtime root below Darwin's Unix-domain path limit.
	root, err := os.MkdirTemp("/tmp", "graith-native-*")
	if err != nil {
		t.Fatal("create short native validation root failed")
	}

	t.Cleanup(func() { _ = os.RemoveAll(root) })

	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")
	runtimeHome := filepath.Join(root, "runtime")

	configFile := filepath.Join(configHome, "graith.toml")
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		t.Fatal("create native validation config directory failed")
	}

	cfg := config.Default()
	cfg.DataDir = dataHome
	cfg.DefaultAgent = "echo"
	cfg.FetchOnCreate = false
	cfg.Agents["echo"] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", "printf '" + nativeReadyText + "\\r\\n'; exec cat"},
		ResumeArgs: []string{"-c", "printf '" + nativeResumeText + "\\r\\n'; exec cat"},
	}

	// EffectiveTOML is a display renderer that materializes optional tool
	// defaults. Preserve unset tools here so validation does not depend on
	// developer-only CLIs that this hermetic test never invokes.
	configData, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal("marshal native validation config failed")
	}

	if err := os.WriteFile(configFile, configData, 0o600); err != nil {
		t.Fatal("write native validation config failed")
	}

	appName := "graith-" + nativeProfile
	integrationSocket := filepath.Join(runtimeHome, appName, "graith.sock")
	env := isolatedNativeEnvironment(configHome, dataHome, runtimeHome)
	// The caller-selected binary was verified as a regular file above.
	cmd := exec.Command(binary, "--config", configFile, "daemon", "start") //nolint:gosec
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = io.Discard

	h := &nativeDaemonHarness{
		t:          t,
		binary:     binary,
		configFile: configFile,
		env:        env,
		socket:     integrationSocket,
		tokenFile:  filepath.Join(dataHome, "human.token"),
		pidFile:    filepath.Join(runtimeHome, appName, "graith.pid"),
		cmd:        cmd,
		done:       make(chan struct{}),
		private:    []string{root, binary},
	}

	cmd.Stderr = &h.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal("start external tagged Graith daemon failed")
	}

	h.identity.PID = cmd.Process.Pid
	go func() {
		completion := "clean-exit"
		if cmd.Wait() != nil {
			completion = "exit-error"
		}

		h.mu.Lock()
		h.completion = completion
		h.mu.Unlock()
		close(h.done)
	}()

	t.Cleanup(h.cleanup)

	startTime, exists, startErr := nativeProcessStartTime(h.identity.PID)
	if startErr != nil || !exists {
		t.Fatalf("capture external tagged Graith daemon identity failed: %s", h.redactedStderr())
	}

	h.identity.StartTime = startTime

	deadline := time.Now().Add(nativeOpTimeout)

	var lastConnectErr error

	for time.Now().Before(deadline) {
		select {
		case <-h.done:
			t.Fatalf("external tagged Graith daemon exited during startup: %s", h.redactedStderr())
		default:
		}

		if pathExists(h.socket) {
			if token, readErr := os.ReadFile(h.tokenFile); readErr == nil && len(bytes.TrimSpace(token)) > 0 {
				pidData, pidErr := os.ReadFile(h.pidFile)

				pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidData)))
				if pidErr == nil && parseErr == nil && pid == h.identity.PID {
					if probe, connectErr := h.tryConnect(); connectErr == nil {
						probe.close()

						return h
					} else {
						lastConnectErr = connectErr
					}
				}
			}
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("external tagged Graith daemon did not become ready: %v", lastConnectErr)

	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func isolatedNativeEnvironment(configHome, dataHome, runtimeHome string) []string {
	return isolatedNativeEnvironmentFrom(os.LookupEnv, configHome, dataHome, runtimeHome)
}

func isolatedNativeEnvironmentFrom(
	lookup func(string) (string, bool),
	configHome, dataHome, runtimeHome string,
) []string {
	allow := []string{
		"PATH", "HOME", "TMPDIR",
		"LANG", "LANGUAGE", "LC_ALL", "LC_COLLATE", "LC_CTYPE", "LC_MESSAGES",
		"LC_MONETARY", "LC_NUMERIC", "LC_TIME",
		"TERM", "GORACE",
	}

	env := make([]string, 0, len(allow)+4)
	for _, key := range allow {
		if value, ok := lookup(key); ok {
			env = append(env, key+"="+value)
		}
	}

	return append(env,
		"GRAITH_PROFILE="+nativeProfile,
		"XDG_CONFIG_HOME="+configHome,
		"XDG_DATA_HOME="+dataHome,
		"XDG_RUNTIME_DIR="+runtimeHome,
	)
}

func (h *nativeDaemonHarness) connect() *nativeControlClient {
	h.t.Helper()

	client, err := h.tryConnect()
	if err != nil {
		h.t.Fatalf("connect to external tagged Graith daemon failed: %v", err)
	}

	h.mu.Lock()
	h.clients = append(h.clients, client)
	h.mu.Unlock()

	return client
}

func (h *nativeDaemonHarness) tryConnect() (*nativeControlClient, error) {
	token, err := os.ReadFile(h.tokenFile)
	if err != nil {
		return nil, errors.New("token unavailable")
	}

	conn, err := net.DialTimeout("unix", h.socket, time.Second)
	if err != nil {
		return nil, errors.New("dial failed")
	}

	client := &nativeControlClient{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
		token:  strings.TrimSpace(string(token)),
	}
	if err := client.send("handshake", protocol.HandshakeMsg{
		Version: protocol.Version, ClientID: "native-validation", Profile: nativeProfile,
		TerminalSize: [2]uint16{80, 24},
	}); err != nil {
		client.close()
		return nil, errors.New("handshake write failed")
	}

	envelope, err := client.readControl()
	if err != nil {
		client.close()
		return nil, errors.New("handshake read failed")
	}

	if envelope.Type != "handshake_ok" {
		client.close()

		if envelope.Type == "handshake_err" {
			var response protocol.HandshakeErrMsg
			if protocol.DecodePayload(envelope, &response) == nil {
				return nil, errors.New("handshake rejected")
			}
		}

		return nil, fmt.Errorf("unexpected handshake response %q", envelope.Type)
	}

	var response protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(envelope, &response); err != nil {
		client.close()
		return nil, errors.New("handshake decode failed")
	}

	client.instance = response.DaemonInstanceID

	return client, nil
}

func (h *nativeDaemonHarness) connectNewGeneration(previous string) *nativeControlClient {
	h.t.Helper()

	// Upgrade acknowledgement precedes the daemon's bounded background, MCP,
	// and session-I/O drains. Match the production client's protocol floor so
	// the integration harness does not kill a valid transition after an ordinary
	// per-request timeout.
	deadline := time.Now().Add(nativeUpgradeTimeout)
	observation := nativeRestartTimeoutObservation{}

	for time.Now().Before(deadline) {
		client, err := h.tryConnect()
		if err == nil {
			if client.instance != "" && client.instance != previous {
				h.mu.Lock()
				h.clients = append(h.clients, client)
				h.mu.Unlock()

				return client
			}

			if client.instance == previous {
				observation.sameGenerationHandshakes++
			}

			client.close()
		} else {
			observation.failedHandshakes++
			observation.lastHandshakeErrorClass = nativeHandshakeErrorClass(err)
		}

		time.Sleep(25 * time.Millisecond)
	}

	h.failNewGenerationTimeout(observation)

	return nil
}

func (h *nativeDaemonHarness) runCLI(args ...string) []byte {
	h.t.Helper()
	commandArgs := append([]string{"--config", h.configFile}, args...)
	cmd := exec.Command(h.binary, commandArgs...)
	cmd.Env = h.env

	var stdout bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		h.t.Fatal("tagged Graith client command failed")
	}

	return stdout.Bytes()
}

func (h *nativeDaemonHarness) helperProcesses() []nativeProcessIdentity {
	h.t.Helper()

	processes, err := nativeHelperChildProcesses(h.identity)
	if err != nil {
		h.t.Fatalf("inspect native screen helper processes failed (%T)", err)
	}

	return processes
}

func signalNativeProcess(identity nativeProcessIdentity, signal syscall.Signal) (bool, error) {
	current, err := nativeProcessIsCurrent(identity)
	if err != nil {
		return false, err
	}

	if !current {
		// The original process exited; a reused PID belongs to somebody else.
		return false, nil
	}

	if err := syscall.Kill(identity.PID, signal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

func (h *nativeDaemonHarness) daemonDone() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

func (h *nativeDaemonHarness) daemonCompletionClass() string {
	if !h.daemonDone() {
		return "running"
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	return h.completion
}

func (h *nativeDaemonHarness) waitForDaemonDone(timeout time.Duration) bool {
	select {
	case <-h.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (h *nativeDaemonHarness) signalDaemon(signal syscall.Signal) (bool, error) {
	if h.daemonDone() {
		return false, nil
	}

	// signalNativeProcess revalidates PID+start immediately before signalling.
	return signalNativeProcess(h.identity, signal)
}

func (h *nativeDaemonHarness) stop() error {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return nil
	}

	h.stopped = true
	clients := append([]*nativeControlClient(nil), h.clients...)
	h.mu.Unlock()

	for _, client := range clients {
		client.close()
	}

	signaled, err := h.signalDaemon(syscall.SIGTERM)
	if err != nil {
		return err
	}

	if !signaled {
		if h.waitForDaemonDone(5 * time.Second) {
			return nil
		}

		return errors.New("tagged daemon exit was not confirmed after signal was skipped")
	}

	if h.waitForDaemonDone(15 * time.Second) {
		return nil
	}

	signaled, killErr := h.signalDaemon(syscall.SIGKILL)
	if killErr != nil {
		return errors.Join(errors.New("tagged daemon shutdown timed out"), killErr)
	}

	if !signaled {
		if h.waitForDaemonDone(5 * time.Second) {
			return errors.New("tagged daemon graceful shutdown timed out")
		}

		return errors.New("tagged daemon exit was not confirmed after kill was skipped")
	}

	if h.waitForDaemonDone(5 * time.Second) {
		return errors.New("tagged daemon graceful shutdown timed out")
	}

	return errors.New("tagged daemon did not exit after bounded kill")
}

func (h *nativeDaemonHarness) cleanup() {
	_ = h.stop()
}

func (h *nativeDaemonHarness) failNewGenerationTimeout(observation nativeRestartTimeoutObservation) {
	h.t.Helper()

	observation.daemonDone = h.daemonDone()
	observation.daemonCompletionClass = h.daemonCompletionClass()

	current, currentErr := nativeProcessIsCurrent(h.identity)
	switch {
	case currentErr != nil:
		observation.daemonCurrent = "unknown"
	case current:
		observation.daemonCurrent = "current"
	default:
		observation.daemonCurrent = "exited"
	}

	cleanupErr := h.stop()
	logTail, logReadErr := readBoundedNativeTail(filepath.Join(filepath.Dir(h.tokenFile), "daemon.log"), nativeDiagnosticTailBytes)
	evidence := classifyNativeRestartLog(logTail)

	h.t.Fatalf("restarted tagged daemon did not present a new generation: %s",
		nativeRestartTimeoutSummary(observation, evidence, cleanupErr, logReadErr))
}

func (h *nativeDaemonHarness) redactedStderr() string {
	message := h.redactDiagnostic(h.stderr.String())
	if message == "" {
		return "no diagnostic output"
	}

	return message
}

func (h *nativeDaemonHarness) redactedControlResponse(envelope protocol.Envelope) string {
	if envelope.Type != "error" {
		return fmt.Sprintf("response type %q", envelope.Type)
	}

	var response protocol.ErrorMsg
	if err := protocol.DecodePayload(envelope, &response); err != nil || response.Message == "" {
		return "unclassified error response"
	}

	return h.redactDiagnostic(response.Message)
}

func (h *nativeDaemonHarness) redactDiagnostic(value string) string {
	message := strings.TrimSpace(value)
	for _, private := range h.private {
		message = strings.ReplaceAll(message, private, "<isolated>")
	}

	return message
}

func (c *nativeControlClient) send(msgType string, payload any) error {
	if err := c.conn.SetDeadline(time.Now().Add(nativeOpTimeout)); err != nil {
		return err
	}

	data, err := protocol.EncodeControlWithToken(msgType, payload, c.token)
	if err != nil {
		return err
	}

	return c.writer.WriteFrame(protocol.ChannelControl, data)
}

func (c *nativeControlClient) readControl() (protocol.Envelope, error) {
	for {
		frame, err := c.reader.ReadFrame()
		if err != nil {
			return protocol.Envelope{}, err
		}

		if frame.Channel == protocol.ChannelControl {
			return protocol.DecodeControl(frame.Payload)
		}
	}
}

func (c *nativeControlClient) request(msgType string, payload any) (protocol.Envelope, error) {
	if err := c.send(msgType, payload); err != nil {
		return protocol.Envelope{}, err
	}

	return c.readControl()
}

func (c *nativeControlClient) close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

func mustControl(t *testing.T, client *nativeControlClient, msgType string, payload any, want string) protocol.Envelope {
	t.Helper()

	envelope, err := client.request(msgType, payload)
	if err != nil || envelope.Type != want {
		t.Fatalf("native %s response was not %s", msgType, want)
	}

	return envelope
}

func createNativeSession(t *testing.T, client *nativeControlClient, name string) protocol.SessionInfo {
	t.Helper()
	envelope := mustControl(t, client, "create", protocol.CreateMsg{
		Name: name, Agent: "echo", NoRepo: true,
	}, "created")

	var info protocol.SessionInfo
	if err := protocol.DecodePayload(envelope, &info); err != nil {
		t.Fatal("decode native session creation response failed")
	}

	return info
}

func soakCreate(client *nativeControlClient, name string) (protocol.SessionInfo, string) {
	envelope, err := client.request("create", protocol.CreateMsg{Name: name, Agent: "echo", NoRepo: true})
	if err != nil || envelope.Type != "created" {
		return protocol.SessionInfo{}, "create"
	}

	var info protocol.SessionInfo
	if protocol.DecodePayload(envelope, &info) != nil {
		return protocol.SessionInfo{}, "create-decode"
	}

	return info, ""
}

func typeNativeInput(client *nativeControlClient, sessionID, input string) error {
	envelope, err := client.request("type", protocol.TypeMsg{SessionID: sessionID, Input: input})
	if err != nil {
		return err
	}

	if envelope.Type != "typed" {
		return errors.New("native type rejected")
	}

	return nil
}

func nativePreview(client *nativeControlClient, sessionID string) (string, error) {
	envelope, err := client.request("screen_preview", protocol.ScreenPreviewMsg{SessionID: sessionID})
	if err != nil || envelope.Type != "screen_preview_response" {
		return "", errors.New("native preview failed")
	}

	var response protocol.ScreenPreviewResponseMsg
	if err := protocol.DecodePayload(envelope, &response); err != nil {
		return "", err
	}

	return response.Preview, nil
}

func waitForPreview(t *testing.T, client *nativeControlClient, sessionID, marker string) {
	t.Helper()

	if !waitForPreviewResult(client, sessionID, marker, nativeOpTimeout) {
		t.Fatal("native preview did not contain expected synthetic marker")
	}
}

func waitForPreviewResult(client *nativeControlClient, sessionID, marker string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		preview, err := nativePreview(client, sessionID)
		if err == nil && strings.Contains(preview, marker) {
			return true
		}

		time.Sleep(25 * time.Millisecond)
	}

	return false
}

func requestNativeSnapshot(client *nativeControlClient, sessionID string) (protocol.ScreenSnapshotResponseMsg, error) {
	envelope, err := client.request("screen_snapshot", protocol.ScreenSnapshotMsg{SessionID: sessionID})
	if err != nil || envelope.Type != "screen_snapshot_response" {
		return protocol.ScreenSnapshotResponseMsg{}, errors.New("native snapshot failed")
	}

	var response protocol.ScreenSnapshotResponseMsg
	if err := protocol.DecodePayload(envelope, &response); err != nil {
		return protocol.ScreenSnapshotResponseMsg{}, err
	}

	return response, nil
}

func nativeSnapshot(t *testing.T, client *nativeControlClient, sessionID string) protocol.ScreenSnapshotResponseMsg {
	t.Helper()

	response, err := requestNativeSnapshot(client, sessionID)
	if err != nil {
		t.Fatal("native snapshot request failed")
	}

	return response
}

func waitForSnapshotSize(t *testing.T, client *nativeControlClient, sessionID string, cols, rows int) {
	t.Helper()

	deadline := time.Now().Add(nativeOpTimeout)
	for time.Now().Before(deadline) {
		response, err := requestNativeSnapshot(client, sessionID)
		if err == nil && response.Cols == cols && response.Rows == rows {
			return
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatal("native snapshot did not reach expected resized geometry")
}

func purgeNativeSession(t *testing.T, client *nativeControlClient, sessionID string) {
	t.Helper()

	if err := purgeNativeSessionResult(client, sessionID); err != nil {
		t.Fatal("purge native validation session failed")
	}
}

func purgeNativeSessionResult(client *nativeControlClient, sessionID string) error {
	envelope, err := client.request("delete", protocol.DeleteMsg{SessionID: sessionID, Purge: true})
	if err != nil {
		return err
	}

	if envelope.Type != "deleted" {
		return fmt.Errorf("native purge returned %q", envelope.Type)
	}

	return nil
}

func purgeNativeSessionBestEffort(client *nativeControlClient, sessionID string) {
	_ = purgeNativeSessionResult(client, sessionID)
}

func waitForHelperCount(t *testing.T, h *nativeDaemonHarness, want int) []nativeProcessIdentity {
	t.Helper()

	processes := waitForHelperCountResult(h, want, nativeOpTimeout)
	if len(processes) != want {
		t.Fatalf("native helper process count = %d, want %d", len(processes), want)
	}

	return processes
}

func waitForHelperCountResult(
	h *nativeDaemonHarness,
	want int,
	timeout time.Duration,
) []nativeProcessIdentity {
	deadline := time.Now().Add(timeout)

	var processes []nativeProcessIdentity

	for time.Now().Before(deadline) {
		processes = h.helperProcesses()
		if len(processes) == want {
			return processes
		}

		time.Sleep(25 * time.Millisecond)
	}

	return processes
}

func daemonFDCount(t *testing.T, identity nativeProcessIdentity) int {
	t.Helper()

	count, err := nativeDaemonFDCount(identity)
	if err != nil {
		t.Fatal("inspect tagged daemon file descriptors failed")
	}

	return count
}

func daemonRSSBytes(t *testing.T, identity nativeProcessIdentity) int64 {
	t.Helper()

	bytes, err := nativeDaemonRSSBytes(identity)
	if err != nil {
		t.Fatal("inspect tagged daemon resident memory failed")
	}

	return bytes
}

func daemonFDGrowthExceeded(baseline, current int) bool {
	return current > baseline
}

func waitForNoFDGrowth(t *testing.T, identity nativeProcessIdentity, baseline int) {
	t.Helper()

	deadline := time.Now().Add(nativeOpTimeout)
	for time.Now().Before(deadline) {
		count := daemonFDCount(t, identity)
		if !daemonFDGrowthExceeded(baseline, count) {
			return
		}

		time.Sleep(25 * time.Millisecond)
	}

	count := daemonFDCount(t, identity)
	if daemonFDGrowthExceeded(baseline, count) {
		t.Fatalf("tagged daemon file-descriptor growth = %d after cleanup (baseline=%d current=%d, want no growth)",
			count-baseline, baseline, count)
	}
}

func waitForProcessExit(t *testing.T, identity nativeProcessIdentity) {
	t.Helper()

	exited, err := waitForProcessExitResult(identity, nativeOpTimeout)
	if err != nil {
		t.Fatal("observe native helper process exit failed")
	}

	if exited {
		return
	}

	t.Fatal("native helper process did not exit within the bounded wait")
}

func waitForProcessExitResult(identity nativeProcessIdentity, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current, err := nativeProcessIsCurrent(identity)
		if err != nil {
			return false, err
		}

		if !current {
			return true, nil
		}

		time.Sleep(25 * time.Millisecond)
	}

	return false, nil
}

func intersectionSize(left, right []nativeProcessIdentity) int {
	set := make(map[nativeProcessIdentity]struct{}, len(left))
	for _, value := range left {
		set[value] = struct{}{}
	}

	count := 0

	for _, value := range right {
		if _, ok := set[value]; ok {
			count++
		}
	}

	return count
}

func uniqueProcessIdentities(values []nativeProcessIdentity) []nativeProcessIdentity {
	set := make(map[nativeProcessIdentity]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}

	result := make([]nativeProcessIdentity, 0, len(set))
	for value := range set {
		result = append(result, value)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].PID == result[j].PID {
			return result[i].StartTime < result[j].StartTime
		}

		return result[i].PID < result[j].PID
	})

	return result
}

func nativeSoakCycles(t *testing.T) int {
	t.Helper()

	value := os.Getenv(nativeSoakCyclesEnv)
	if value == "" {
		return 12
	}

	cycles, err := strconv.Atoi(value)
	if err != nil || cycles < 1 || cycles > 1000 {
		t.Fatalf("%s must be an integer from 1 to 1000", nativeSoakCyclesEnv)
	}

	return cycles
}

func nativeSoakTimeout(t *testing.T) time.Duration {
	t.Helper()

	value := os.Getenv(nativeSoakTimeoutEnv)
	if value == "" {
		return 3 * time.Minute
	}

	timeout, err := time.ParseDuration(value)
	if err != nil || timeout < time.Minute || timeout > time.Hour {
		t.Fatalf("%s must be a duration from 1m to 1h", nativeSoakTimeoutEnv)
	}

	return timeout
}

func nativeLongSoak() bool {
	return os.Getenv(nativeLongSoakEnv) == "1"
}
