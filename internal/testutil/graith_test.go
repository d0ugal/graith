package testutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/testutil"
)

func TestRunWithIsolatedGraithContainsPathsAndRestoresEnvironment(t *testing.T) {
	hostRoot := t.TempDir()

	original := map[string]string{
		"XDG_CONFIG_HOME":     filepath.Join(hostRoot, "config"),
		"XDG_DATA_HOME":       filepath.Join(hostRoot, "data"),
		"XDG_RUNTIME_DIR":     filepath.Join(hostRoot, "runtime"),
		"GRAITH_PROFILE":      "host-profile",
		"GRAITH_TOKEN":        "host-token",
		"GRAITH_SESSION_ID":   "host-session-id",
		"GRAITH_SESSION_NAME": "host-session-name",
	}
	for name, value := range original {
		t.Setenv(name, value)
	}

	const wantCode = 37

	var isolatedRoot string

	code := testutil.RunWithIsolatedGraith(func() int {
		isolatedRoot = filepath.Dir(os.Getenv("XDG_CONFIG_HOME"))

		paths, err := config.ResolvePaths()
		if err != nil {
			t.Errorf("ResolvePaths() error: %v", err)

			return wantCode
		}

		if paths.Profile == "" || paths.Profile == original["GRAITH_PROFILE"] {
			t.Errorf("isolated profile = %q, want a non-host profile", paths.Profile)
		}

		if len(paths.SocketPath) >= 104 {
			t.Errorf("isolated socket path is %d bytes, want less than macOS limit 104: %s", len(paths.SocketPath), paths.SocketPath)
		}

		for name, path := range map[string]string{
			"config file": paths.ConfigFile,
			"data dir":    paths.DataDir,
			"runtime dir": paths.RuntimeDir,
			"socket":      paths.SocketPath,
			"PID file":    paths.PIDFile,
			"state file":  paths.StateFile,
			"human token": paths.HumanTokenFile,
			"log dir":     paths.LogDir,
			"daemon log":  paths.DaemonLog,
			"messages DB": paths.MessagesDB,
			"todos DB":    paths.TodosDB,
			"temp dir":    paths.TmpDir,
		} {
			if !pathWithin(isolatedRoot, path) {
				t.Errorf("%s %q is outside isolated root %q", name, path, isolatedRoot)
			}
		}

		if token, present := os.LookupEnv("GRAITH_TOKEN"); !present || token != "" {
			t.Errorf("GRAITH_TOKEN = %q, present=%t; want explicitly empty", token, present)
		}

		for _, name := range []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME"} {
			if value, present := os.LookupEnv(name); present {
				t.Errorf("%s = %q, want unset", name, value)
			}
		}

		if err := os.WriteFile(filepath.Join(isolatedRoot, "canny"), []byte("temporary\n"), 0o600); err != nil {
			t.Errorf("write isolated marker: %v", err)
		}

		return wantCode
	})

	if code != wantCode {
		t.Fatalf("RunWithIsolatedGraith() code = %d, want %d", code, wantCode)
	}

	for name, want := range original {
		if got, present := os.LookupEnv(name); !present || got != want {
			t.Errorf("%s after cleanup = %q, present=%t; want %q", name, got, present, want)
		}
	}

	if _, err := os.Stat(isolatedRoot); !os.IsNotExist(err) {
		t.Errorf("isolated root still exists after cleanup: %v", err)
	}
}

func TestRunWithIsolatedGraithRestoresUnsetEnvironment(t *testing.T) {
	type originalValue struct {
		value   string
		present bool
	}

	variables := []string{
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "GRAITH_PROFILE", "GRAITH_TOKEN",
		"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME",
	}

	original := make(map[string]originalValue, len(variables))
	for _, name := range variables {
		value, present := os.LookupEnv(name)
		original[name] = originalValue{value: value, present: present}

		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		for name, entry := range original {
			if entry.present {
				_ = os.Setenv(name, entry.value)
			} else {
				_ = os.Unsetenv(name)
			}
		}
	})

	code := testutil.RunWithIsolatedGraith(func() int { return 0 })
	if code != 0 {
		t.Fatalf("RunWithIsolatedGraith() code = %d, want 0", code)
	}

	for _, name := range variables {
		if value, present := os.LookupEnv(name); present {
			t.Errorf("%s after cleanup = %q, want unset", name, value)
		}
	}
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}

	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
