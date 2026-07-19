package commandpolicy

import "testing"

func TestShellCommandScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, tool, input, want string
		inScope                 bool
		wantErr                 bool
	}{
		{name: "claude", tool: "Bash", input: `{"command":"echo braw"}`, want: "echo braw", inScope: true},
		{name: "codex", tool: "exec_command", input: `{"cmd":"echo canny"}`, want: "echo canny", inScope: true},
		{name: "outside scope", tool: "Read", input: `{}`, inScope: false},
		{name: "malformed", tool: "shell", input: `{`, inScope: true, wantErr: true},
		{name: "empty", tool: "run_shell_command", input: `{}`, inScope: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, inScope, err := shellCommand(tt.tool, tt.input)
			if got != tt.want || inScope != tt.inScope || (err != nil) != tt.wantErr {
				t.Fatalf("shellCommand() = (%q, %v, %v), want (%q, %v, error=%v)", got, inScope, err, tt.want, tt.inScope, tt.wantErr)
			}
		})
	}
}

func TestBackendByNameRejectsInteractiveAndAllowAllBackends(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "prompt", "auto", "command", "external"} {
		if _, err := BackendByName(name); err == nil {
			t.Fatalf("BackendByName(%q) unexpectedly succeeded", name)
		}
	}
}
