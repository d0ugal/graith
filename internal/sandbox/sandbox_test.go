package sandbox

import (
	"runtime"
	"testing"
)

func TestWrapBasic(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/home/user/worktree",
		EnvKeys:     []string{"GRAITH_SESSION_ID", "TERM"},
	}
	cmd, args := Wrap("claude", []string{"--session-id", "abc"}, opts)

	if cmd != "safehouse" {
		t.Fatalf("cmd = %q, want safehouse", cmd)
	}

	want := []string{
		"--workdir", "/home/user/worktree",
		"--env-pass", "GRAITH_SESSION_ID,TERM",
		"--", "claude", "--session-id", "abc",
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
		WorktreeDir: "/tmp/wt",
		Features:    []string{"ssh", "process-control"},
		EnvKeys:     []string{"TERM"},
	}
	cmd, args := Wrap("codex", []string{}, opts)

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
		WorktreeDir: "/tmp/wt",
		ReadDirs:    []string{"/home/user/Code", "/opt/shared"},
		EnvKeys:     []string{"TERM"},
	}
	_, args := Wrap("claude", nil, opts)

	found := false
	for i, a := range args {
		if a == "--add-dirs-ro" && i+1 < len(args) {
			if args[i+1] != "/home/user/Code:/opt/shared" {
				t.Errorf("--add-dirs-ro value = %q, want %q", args[i+1], "/home/user/Code:/opt/shared")
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
		WorktreeDir: "/tmp/wt",
		WriteDirs:   []string{"/tmp/extra"},
		EnvKeys:     []string{"TERM"},
	}
	_, args := Wrap("claude", nil, opts)

	found := false
	for i, a := range args {
		if a == "--add-dirs" && i+1 < len(args) {
			if args[i+1] != "/tmp/extra" {
				t.Errorf("--add-dirs value = %q, want %q", args[i+1], "/tmp/extra")
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
		WorktreeDir: "/tmp/wt",
	}
	_, args := Wrap("claude", nil, opts)

	for _, a := range args {
		if a == "--env-pass" {
			t.Error("--env-pass should not be present when EnvKeys is empty")
		}
	}
}

func TestWrapCommandAndArgsAfterSeparator(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/wt",
		EnvKeys:     []string{"TERM"},
	}
	_, args := Wrap("codex", []string{"resume", "--last"}, opts)

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
		WorktreeDir:      "/tmp/wt",
		SafehouseCommand: "/usr/local/bin/safehouse",
		EnvKeys:          []string{"TERM"},
	}
	cmd, _ := Wrap("claude", nil, opts)

	if cmd != "/usr/local/bin/safehouse" {
		t.Fatalf("cmd = %q, want /usr/local/bin/safehouse", cmd)
	}
}

func TestWrapSharedWorktreeReadOnly(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/scratch/abc123",
		ReadDirs:    []string{"/shared/worktree"},
		EnvKeys:     []string{"TERM"},
	}
	_, args := Wrap("claude", nil, opts)

	foundWorkdir := false
	foundReadDirs := false
	for i, a := range args {
		if a == "--workdir" && i+1 < len(args) {
			if args[i+1] != "/tmp/scratch/abc123" {
				t.Errorf("--workdir = %q, want /tmp/scratch/abc123", args[i+1])
			}
			foundWorkdir = true
		}
		if a == "--add-dirs-ro" && i+1 < len(args) {
			if args[i+1] != "/shared/worktree" {
				t.Errorf("--add-dirs-ro = %q, want /shared/worktree", args[i+1])
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
