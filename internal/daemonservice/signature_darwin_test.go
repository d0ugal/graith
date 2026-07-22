//go:build darwin

package daemonservice

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCodesignDetails(t *testing.T) {
	tests := []struct {
		name        string
		requirement string
		line        string
	}{
		{name: "developer id", requirement: `identifier "net.graith.service" and anchor apple generic`, line: `designated => identifier "net.graith.service" and anchor apple generic`},
		{name: "development", requirement: `cdhash H"canny"`, line: `# designated => cdhash H"canny"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := parseCodesignDetails([]byte("Identifier=net.graith.service\nTeamIdentifier=BRAWTEAM\n" + test.line + "\n"))
			if info.Identifier != ServiceManifest().BundleIdentifier || info.TeamID != "BRAWTEAM" || info.Requirement != test.requirement {
				t.Fatalf("unexpected signature details: %#v", info)
			}
		})
	}
}

func TestLaunchctlReportsOnlyExplicitMissingService(t *testing.T) {
	if !launchctlReportsMissing(`Bad request. Could not find service "net.graith.service.daemon" in domain for user gui: 501`) {
		t.Fatal("launchctl missing-service response was not recognized")
	}

	for _, output := range []string{"Not privileged", "Operation not permitted", "domain unavailable"} {
		if launchctlReportsMissing(output) {
			t.Fatalf("launchctl error %q was treated as an absent job", output)
		}
	}
}

func TestParseLaunchctlJobStateIncludesResolvedProgram(t *testing.T) {
	state := parseLaunchctlJobState(`
	program identifier = Contents/MacOS/gr (mode: 2)
	parent bundle identifier = net.graith.service
	parent bundle version = 1473
	state = running
	pid = 4242
`)
	if !state.Present || !state.Running || state.PID != 4242 || state.ProgramIdentifier != "Contents/MacOS/gr" || state.ParentBundleIdentifier != ServiceManifest().BundleIdentifier || state.ParentBundleVersion != "1473" {
		t.Fatalf("parsed launchd job = %#v", state)
	}
}

func TestLimitedControllerOutputFailsClosed(t *testing.T) {
	buffer := &bytes.Buffer{}

	limited := &limitedBuffer{buffer: buffer, limit: 4}
	if _, err := limited.Write([]byte("braw")); err != nil {
		t.Fatal(err)
	}

	if _, err := limited.Write([]byte("x")); err == nil {
		t.Fatal("controller output beyond its bound was accepted")
	}
}

func TestDarwinControllerRejectsMismatchedResponseEcho(t *testing.T) {
	controllerPath := filepath.Join(t.TempDir(), "canny-controller")

	script := []byte("#!/bin/sh\nprintf '%s\\n' '{\"operation\":\"unregister\",\"service\":\"63\",\"status\":\"enabled\"}'\n")
	if err := os.WriteFile(controllerPath, script, 0o600); err != nil {
		t.Fatal(err)
	}

	// #nosec G302 -- this test-only controller must be executable and lives in t.TempDir().
	if err := os.Chmod(controllerPath, 0o700); err != nil {
		t.Fatal(err)
	}

	definition := Definitions()[0]

	_, err := (DarwinController{}).invoke(context.Background(), controllerPath, definition, "status")
	if err == nil || !strings.Contains(err.Error(), "response mismatch") {
		t.Fatalf("controller response mismatch = %v", err)
	}
}

func TestDarwinControllerInvokeRejectsGoTestBeforeMutatingCommand(t *testing.T) {
	controllerPath := filepath.Join(t.TempDir(), "dreich-controller")
	markerPath := controllerPath + ".called"

	script := []byte("#!/bin/sh\nprintf called > \"$0.called\"\n")
	if err := os.WriteFile(controllerPath, script, 0o600); err != nil {
		t.Fatal(err)
	}

	// #nosec G302 -- this test-only controller must be executable and lives in t.TempDir().
	if err := os.Chmod(controllerPath, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := (DarwinController{}).invoke(context.Background(), controllerPath, Definitions()[0], "unregister")
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("mutating controller invoke error = %v, want Go-test refusal", err)
	}

	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("Go-test refusal executed controller command: %v", statErr)
	}
}
