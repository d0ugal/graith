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
