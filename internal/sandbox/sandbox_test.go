package sandbox

import (
	"runtime"
	"testing"
)

func TestWrapBasic(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/hame/user/bothy",
		EnvKeys:     []string{"GRAITH_SESSION_ID", "TERM"},
	}

	cmd, args, err := Wrap("claude", []string{"--session-id", "braw"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != "safehouse" {
		t.Fatalf("cmd = %q, want safehouse", cmd)
	}

	want := []string{
		"--workdir", "/hame/user/bothy",
		"--env-pass", "GRAITH_SESSION_ID,TERM",
		"--", "claude", "--session-id", "braw",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}

	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestWrapWithFeatures(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/bothy",
		Features:    []string{"ssh", "process-control"},
		EnvKeys:     []string{"TERM"},
	}

	cmd, args, err := Wrap("codex", []string{}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != "safehouse" {
		t.Fatalf("cmd = %q, want safehouse", cmd)
	}

	found := false

	for i, a := range args {
		if a == "--enable" && i+1 < len(args) {
			if args[i+1] != "ssh,process-control" {
				t.Errorf("--enable value = %q, want %q", args[i+1], "ssh,process-control")
			}

			found = true

			break
		}
	}

	if !found {
		t.Errorf("--enable not found in args: %v", args)
	}
}

func TestWrapWithReadDirs(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/bothy",
		ReadDirs:    []string{"/hame/user/glen", "/opt/wynd"},
		EnvKeys:     []string{"TERM"},
	}

	_, args, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false

	for i, a := range args {
		if a == "--add-dirs-ro" && i+1 < len(args) {
			if args[i+1] != "/hame/user/glen:/opt/wynd" {
				t.Errorf("--add-dirs-ro value = %q, want %q", args[i+1], "/hame/user/glen:/opt/wynd")
			}

			found = true

			break
		}
	}

	if !found {
		t.Errorf("--add-dirs-ro not found in args: %v", args)
	}
}

func TestWrapWithWriteDirs(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/bothy",
		WriteDirs:   []string{"/tmp/croft"},
		EnvKeys:     []string{"TERM"},
	}

	_, args, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false

	for i, a := range args {
		if a == "--add-dirs" && i+1 < len(args) {
			if args[i+1] != "/tmp/croft" {
				t.Errorf("--add-dirs value = %q, want %q", args[i+1], "/tmp/croft")
			}

			found = true

			break
		}
	}

	if !found {
		t.Errorf("--add-dirs not found in args: %v", args)
	}
}

func TestWrapNoEnvKeys(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/bothy",
	}

	_, args, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, a := range args {
		if a == "--env-pass" {
			t.Error("--env-pass should not be present when EnvKeys is empty")
		}
	}
}

func TestWrapCommandAndArgsAfterSeparator(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/bothy",
		EnvKeys:     []string{"TERM"},
	}

	_, args, err := Wrap("codex", []string{"resume", "--last"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sepIdx := -1

	for i, a := range args {
		if a == "--" {
			sepIdx = i
			break
		}
	}

	if sepIdx == -1 {
		t.Fatal("separator -- not found in args")
	}

	tail := args[sepIdx+1:]
	if len(tail) != 3 || tail[0] != "codex" || tail[1] != "resume" || tail[2] != "--last" {
		t.Errorf("args after -- = %v, want [codex resume --last]", tail)
	}
}

func TestWrapCustomCommand(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir:      "/tmp/bothy",
		SafehouseCommand: "/usr/local/bin/safehouse",
		EnvKeys:          []string{"TERM"},
	}

	cmd, _, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != "/usr/local/bin/safehouse" {
		t.Fatalf("cmd = %q, want /usr/local/bin/safehouse", cmd)
	}
}

func TestWrapSharedWorktreeReadOnly(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/bothy/braw123",
		ReadDirs:    []string{"/shared/glen"},
		EnvKeys:     []string{"TERM"},
	}

	_, args, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundWorkdir := false
	foundReadDirs := false

	for i, a := range args {
		if a == "--workdir" && i+1 < len(args) {
			if args[i+1] != "/tmp/bothy/braw123" {
				t.Errorf("--workdir = %q, want /tmp/bothy/braw123", args[i+1])
			}

			foundWorkdir = true
		}

		if a == "--add-dirs-ro" && i+1 < len(args) {
			if args[i+1] != "/shared/glen" {
				t.Errorf("--add-dirs-ro = %q, want /shared/glen", args[i+1])
			}

			foundReadDirs = true
		}
	}

	if !foundWorkdir {
		t.Error("--workdir not found in args")
	}

	if !foundReadDirs {
		t.Error("--add-dirs-ro not found in args")
	}
}

func TestWrapRejectsColonInWorkdir(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/thrawn:bothy:colons",
		EnvKeys:     []string{"TERM"},
	}

	_, _, err := Wrap("claude", nil, opts)
	if err == nil {
		t.Fatal("expected error for colon in workdir path")
	}
}

func TestWrapRejectsColonInReadDirs(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/bothy",
		ReadDirs:    []string{"/bonnie/glen", "/thrawn:glen"},
		EnvKeys:     []string{"TERM"},
	}

	_, _, err := Wrap("claude", nil, opts)
	if err == nil {
		t.Fatal("expected error for colon in read dir path")
	}
}

func TestWrapRejectsColonInWriteDirs(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/bothy",
		WriteDirs:   []string{"/thrawn:croft"},
		EnvKeys:     []string{"TERM"},
	}

	_, _, err := Wrap("claude", nil, opts)
	if err == nil {
		t.Fatal("expected error for colon in write dir path")
	}
}

func TestWrapAcceptsPathsWithoutColons(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/bothy",
		ReadDirs:    []string{"/hame/user/glen", "/opt/wynd"},
		WriteDirs:   []string{"/tmp/croft"},
		EnvKeys:     []string{"TERM"},
	}

	_, _, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error for valid paths: %v", err)
	}
}

func TestAvailableOnlyOnDarwin(t *testing.T) {
	result := Available()
	if runtime.GOOS != "darwin" && result {
		t.Error("Available() should be false on non-darwin")
	}
}

func TestAvailableCommandCustomBinary(t *testing.T) {
	result := AvailableCommand("this-binary-does-not-exist-anywhere")
	if result {
		t.Error("AvailableCommand should be false for nonexistent binary")
	}

	if runtime.GOOS == "darwin" {
		result = AvailableCommand("ls")
		if !result {
			t.Error("AvailableCommand('ls') should be true on darwin")
		}
	}
}
