package chrome

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var defaultPaths = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
}

func FindChrome() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("chrome remote debugging is only supported on macOS")
	}

	for _, p := range defaultPaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	if p, err := exec.LookPath("google-chrome"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("chromium"); err == nil {
		return p, nil
	}

	return "", fmt.Errorf("Chrome not found; install Chrome or set chrome.path in config")
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find free port: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

type StartOpts struct {
	ChromePath string
	Port       int
}

type Instance struct {
	cmd     *exec.Cmd
	port    int
	dir     string
	stopped bool
}

func Start(opts StartOpts) (*Instance, error) {
	chromePath := opts.ChromePath
	if chromePath == "" {
		var err error
		chromePath, err = FindChrome()
		if err != nil {
			return nil, err
		}
	}

	port := opts.Port
	if port == 0 {
		var err error
		port, err = freePort()
		if err != nil {
			return nil, err
		}
	}

	dir, err := os.MkdirTemp("", "graith-chrome-*")
	if err != nil {
		return nil, fmt.Errorf("create chrome temp dir: %w", err)
	}

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-debugging-address=127.0.0.1",
		fmt.Sprintf("--user-data-dir=%s", dir),
		"--headless=new",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-default-apps",
	}

	cmd := exec.Command(chromePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start chrome: %w", err)
	}

	inst := &Instance{
		cmd:  cmd,
		port: port,
		dir:  dir,
	}

	if err := inst.waitReady(10 * time.Second); err != nil {
		inst.Stop()
		return nil, err
	}

	return inst, nil
}

func (i *Instance) waitReady(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", i.port)
	client := &http.Client{Timeout: 500 * time.Millisecond}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("chrome did not become ready within %s on port %d", timeout, i.port)
		case <-ticker.C:
			resp, err := client.Get(url)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

func (i *Instance) Port() int {
	return i.port
}

func (i *Instance) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", i.port)
}

func (i *Instance) PID() int {
	if i.cmd.Process == nil {
		return 0
	}
	return i.cmd.Process.Pid
}

func (i *Instance) Dir() string {
	return i.dir
}

func (i *Instance) Stop() error {
	if i.stopped {
		return nil
	}
	i.stopped = true

	var errs []string

	if i.cmd.Process != nil {
		pgid := i.cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)

		done := make(chan struct{})
		go func() {
			i.cmd.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
		}
	}

	if err := os.RemoveAll(i.dir); err != nil {
		errs = append(errs, fmt.Sprintf("remove chrome dir: %v", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// StopByPID kills a Chrome process by PID and cleans up its user-data directory.
// Used for recovering orphaned Chrome processes after daemon restart.
func StopByPID(pid int, dir string) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(500 * time.Millisecond)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}
