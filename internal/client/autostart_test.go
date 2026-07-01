package client

import (
	"os"
	"testing"
)

func TestDaemonStartArgsStripsConfigInsideSession(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "braw-session-123")

	args := daemonStartArgs("/tmp/evil.toml")

	for _, arg := range args {
		if arg == "--config" || arg == "/tmp/evil.toml" {
			t.Fatalf("daemon start args should not contain --config inside a session, got %v", args)
		}
	}

	if len(args) != 2 || args[0] != "daemon" || args[1] != "start" {
		t.Errorf("expected [daemon start], got %v", args)
	}
}

func TestDaemonStartArgsAllowsConfigOutsideSession(t *testing.T) {
	if v, ok := os.LookupEnv("GRAITH_SESSION_ID"); ok {
		t.Cleanup(func() { _ = os.Setenv("GRAITH_SESSION_ID", v) })
	}

	_ = os.Unsetenv("GRAITH_SESSION_ID")

	args := daemonStartArgs("/home/user/custom.toml")

	if len(args) != 4 || args[2] != "--config" || args[3] != "/home/user/custom.toml" {
		t.Errorf("expected [daemon start --config /home/user/custom.toml], got %v", args)
	}
}

func TestDaemonStartArgsStripsConfigWhenSessionIDEmpty(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")

	args := daemonStartArgs("/tmp/evil.toml")

	for _, arg := range args {
		if arg == "--config" || arg == "/tmp/evil.toml" {
			t.Fatalf("daemon start args should not contain --config when GRAITH_SESSION_ID is set (even empty), got %v", args)
		}
	}
}

func TestDaemonStartArgsEmptyConfigFile(t *testing.T) {
	args := daemonStartArgs("")

	if len(args) != 2 || args[0] != "daemon" || args[1] != "start" {
		t.Errorf("expected [daemon start] for empty config, got %v", args)
	}
}
