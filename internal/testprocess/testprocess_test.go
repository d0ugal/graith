package testprocess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsGoTestBinary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		executable string
		reported   bool
		want       bool
	}{
		{name: "standard name", executable: "/tmp/braw.test", want: true},
		{name: "custom name", executable: "/tmp/canny-runner", reported: true, want: true},
		{name: "production", executable: "/usr/local/bin/gr", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := IsGoTestBinary(test.executable, test.reported); got != test.want {
				t.Fatalf("IsGoTestBinary(%q, %t) = %t, want %t", test.executable, test.reported, got, test.want)
			}
		})
	}
}

func TestRefuseDaemonLifecycleMutationRejectsCurrentTestProcess(t *testing.T) {
	err := RefuseDaemonLifecycleMutation("stop braw daemon")
	if err == nil {
		t.Fatal("RefuseDaemonLifecycleMutation() allowed a Go test process")
	}

	if message := err.Error(); !strings.Contains(message, "stop braw daemon") || !strings.Contains(message, "Go test binary") {
		t.Fatalf("RefuseDaemonLifecycleMutation() error = %q, want operation and Go-test diagnosis", message)
	}
}

func TestRefuseDaemonLifecycleMutationRejectsSessionContext(t *testing.T) {
	resetHumanLifecycleAuthority()

	err := refuseDaemonLifecycleMutation("stop daemon", "/usr/local/bin/gr", false)
	if err == nil || !strings.Contains(err.Error(), "positive human lifecycle authority") {
		t.Fatalf("RefuseDaemonLifecycleMutation() error = %v, want missing-authority refusal", err)
	}
}

func TestRefuseDaemonLifecycleMutationAllowsHumanContext(t *testing.T) {
	resetHumanLifecycleAuthority()
	markHumanLifecycleAuthority()

	defer resetHumanLifecycleAuthority()

	if err := refuseDaemonLifecycleMutation("stop daemon", "/usr/local/bin/gr", false); err != nil {
		t.Fatalf("human lifecycle mutation refused: %v", err)
	}
}

func TestRefuseDaemonLifecycleMutationCleanEnvironmentStillDenied(t *testing.T) {
	resetHumanLifecycleAuthority()

	defer resetHumanLifecycleAuthority()

	if err := refuseDaemonLifecycleMutation("remove service", "/usr/local/bin/gr", false); err == nil {
		t.Fatal("clean environment lifecycle mutation was allowed without positive authority")
	}
}

func TestEstablishHumanLifecycleAuthorityRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "human.token")
	if err := os.WriteFile(target, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := EstablishHumanLifecycleAuthorityFromFile(link); err == nil {
		t.Fatal("EstablishHumanLifecycleAuthorityFromFile() followed a symlink")
	}
}
