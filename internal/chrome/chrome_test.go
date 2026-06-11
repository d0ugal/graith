package chrome

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestFreePort(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort() error: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Fatalf("freePort() returned invalid port: %d", port)
	}

	port2, err := freePort()
	if err != nil {
		t.Fatalf("freePort() second call error: %v", err)
	}
	if port2 <= 0 || port2 > 65535 {
		t.Fatalf("freePort() second call returned invalid port: %d", port2)
	}
}

func TestFindChrome(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("FindChrome only works on macOS")
	}

	path, err := FindChrome()
	if err != nil {
		t.Skipf("Chrome not installed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("FindChrome returned %q but stat failed: %v", path, err)
	}
}

func TestInstanceURL(t *testing.T) {
	inst := &Instance{port: 9222}
	got := inst.URL()
	want := "http://127.0.0.1:9222"
	if got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
}

func TestInstancePort(t *testing.T) {
	inst := &Instance{port: 12345}
	if got := inst.Port(); got != 12345 {
		t.Errorf("Port() = %d, want 12345", got)
	}
}

func TestStartStop(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Chrome remote debugging only supported on macOS")
	}
	if _, err := FindChrome(); err != nil {
		t.Skipf("Chrome not installed: %v", err)
	}

	inst, err := Start(StartOpts{})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer inst.Stop()

	if inst.Port() <= 0 {
		t.Fatal("expected a positive port")
	}
	if inst.PID() <= 0 {
		t.Fatal("expected a positive PID")
	}

	resp, err := http.Get(inst.URL() + "/json/version")
	if err != nil {
		t.Fatalf("Chrome DevTools endpoint not reachable: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}

	if err := inst.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	conn, err := net.Dial("tcp", inst.URL()[len("http://"):])
	if err == nil {
		conn.Close()
		t.Fatal("Chrome still listening after Stop()")
	}
}

func TestStartWithExplicitPort(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Chrome remote debugging only supported on macOS")
	}
	if _, err := FindChrome(); err != nil {
		t.Skipf("Chrome not installed: %v", err)
	}

	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort() error: %v", err)
	}

	inst, err := Start(StartOpts{Port: port})
	if err != nil {
		t.Fatalf("Start(port=%d) error: %v", port, err)
	}
	defer inst.Stop()

	if inst.Port() != port {
		t.Errorf("Port() = %d, want %d", inst.Port(), port)
	}
}

func TestStartBadPath(t *testing.T) {
	_, err := Start(StartOpts{ChromePath: "/nonexistent/chrome"})
	if err == nil {
		t.Fatal("expected error for bad chrome path")
	}
}

func TestFindChromeNotDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this test verifies non-darwin behavior")
	}
	_, err := FindChrome()
	if err == nil {
		t.Fatal("expected error on non-darwin")
	}
}

func TestWaitReadyTimeout(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only runs on macOS")
	}

	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort() error: %v", err)
	}

	// Find a command that stays alive but doesn't listen on the port
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer cmd.Process.Kill()

	inst := &Instance{
		cmd:  cmd,
		port: port,
		dir:  t.TempDir(),
	}

	err = inst.waitReady(500 * time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
