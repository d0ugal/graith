package processidentity

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/d0ugal/graith/internal/tools"
)

func TestIsGraithDaemonFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		pid        int
		alive      bool
		process    string
		processErr error
		want       bool
		wantPS     bool
	}{
		{name: "pid zero", pid: 0},
		{name: "pid one", pid: 1},
		{name: "negative pid", pid: -1},
		{name: "dead process", pid: 4242},
		{name: "ps failure", pid: 4242, alive: true, processErr: errors.New("dreich ps"), wantPS: true},
		{name: "empty process name", pid: 4242, alive: true, wantPS: true},
		{name: "unexpected process", pid: 4242, alive: true, process: "/usr/bin/braw\n", wantPS: true},
		{name: "prefix match", pid: 4242, alive: true, process: "/opt/bin/graith-helper\n", wantPS: true},
		{name: "suffix match", pid: 4242, alive: true, process: "/opt/bin/not-gr\n", wantPS: true},
		{name: "gr basename", pid: 4242, alive: true, process: "/opt/graith/bin/gr\n", want: true, wantPS: true},
		{name: "graith basename", pid: 4242, alive: true, process: "  /opt/graith/bin/graith  \n", want: true, wantPS: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			aliveCalled := false
			psCalled := false
			got := isGraithDaemon(test.pid, func(int) bool {
				aliveCalled = true

				return test.alive
			}, func(string, ...string) ([]byte, error) {
				psCalled = true

				return []byte(test.process), test.processErr
			})

			if got != test.want {
				t.Errorf("isGraithDaemon() = %t, want %t", got, test.want)
			}

			if aliveCalled != (test.pid > 1) {
				t.Errorf("alive check called = %t, want %t", aliveCalled, test.pid > 1)
			}

			if psCalled != test.wantPS {
				t.Errorf("ps called = %t, want %t", psCalled, test.wantPS)
			}
		})
	}
}

func TestProcessCommandUsesConfiguredPSContract(t *testing.T) {
	tools.Configure(tools.Config{PS: "/bothy/bin/ps"})
	t.Cleanup(tools.Reset)

	var (
		name string
		args []string
	)

	output, err := processCommand(4242, func(gotName string, gotArgs ...string) ([]byte, error) {
		name = gotName
		args = append([]string(nil), gotArgs...)

		return []byte("gr\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if name != "/bothy/bin/ps" {
		t.Errorf("ps executable = %q, want %q", name, "/bothy/bin/ps")
	}

	wantArgs := []string{"-p", "4242", "-o", "comm="}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("ps arguments = %q, want %q", args, wantArgs)
	}

	if string(output) != "gr\n" {
		t.Errorf("ps output = %q, want %q", output, "gr\\n")
	}
}

func TestIsGraithDaemonRejectsCurrentProcess(t *testing.T) {
	if IsGraithDaemon(os.Getpid()) {
		t.Fatal("the Go test process was identified as a graith daemon")
	}
}
