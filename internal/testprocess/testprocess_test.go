package testprocess

import (
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
	err := refuseDaemonLifecycleMutation("stop daemon", "/usr/local/bin/gr", false, []string{"GR_AGENT_MODE=0", "GRAITH_SESSION_ID=braw-session"})
	if err == nil || !strings.Contains(err.Error(), "agent execution context") {
		t.Fatalf("RefuseDaemonLifecycleMutation() error = %v, want agent-context refusal", err)
	}
}

func TestRefuseDaemonLifecycleMutationAllowsHumanContext(t *testing.T) {
	if err := refuseDaemonLifecycleMutation("stop daemon", "/usr/local/bin/gr", false, []string{"GR_AGENT_MODE=0"}); err != nil {
		t.Fatalf("human lifecycle mutation refused: %v", err)
	}
}
