package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func lifecycleGenerationConfig(t *testing.T, recordPath, generation string) *config.Config {
	t.Helper()

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Sandbox.Enabled = false
	cfg.AgentPrompt = generation + " prompt"

	injectPrompt := true
	recorderScript := lifecycleRecorder(t)
	agent := config.Agent{
		Command:      "/bin/sh",
		Args:         []string{recorderScript, generation + "-base"},
		ResumeArgs:   []string{recorderScript, generation + "-resume"},
		ForkArgs:     []string{recorderScript, generation + "-fork"},
		Env:          map[string]string{"GRAITH_RECORD": recordPath},
		InjectPrompt: &injectPrompt,
	}

	if generation == "old" {
		agent.Hooks = &config.AgentHookConfig{
			Mechanism: config.HookMechanismCodexConfig,
			EventArgs: []string{"old-hook-{hook_event}"},
			TrustArgs: []string{"old-hook-trust"},
		}
		agent.MCP = &config.AgentMCPConfig{
			Mechanism:  config.MCPMechanismCodexConfig,
			ServerArgs: []string{"old-mcp-{mcp_name}"},
		}
		agent.PromptInjection = config.PromptInjectionDeveloperInstructions
		agent.PromptInjectionArgs = []string{"old-prompt-{prompt}"}
	} else {
		// Deliberately incompatible phase-2 adapters. A mixed generation would
		// emit these markers (and different artifact formats) into the old launch.
		agent.Hooks = &config.AgentHookConfig{
			Mechanism:    config.HookMechanismClaudeSettings,
			SettingsArgs: []string{"new-hook-{path}"},
		}
		agent.MCP = &config.AgentMCPConfig{
			Mechanism:  config.MCPMechanismClaudeConfig,
			ConfigArgs: []string{"new-mcp-{path}"},
		}
		agent.PromptInjection = config.PromptInjectionAppendSystemPrompt
		agent.PromptInjectionArgs = []string{"new-prompt-{prompt}"}
	}

	cfg.Agents["thrawn"] = agent

	return cfg
}

func lifecycleRecorder(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "record-argv.sh")

	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GRAITH_RECORD\"\nsleep 30\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func runPausedLifecycle(t *testing.T, sm *SessionManager, operation string, oldCfg, newCfg *config.Config, run func() (SessionState, error)) SessionState {
	t.Helper()

	entered := make(chan *config.Config, 1)
	release := make(chan struct{})
	sm.launchPhase2Hook = func(got string, cfgSnapshot *config.Config) {
		if got != operation {
			return
		}

		entered <- cfgSnapshot

		<-release
	}

	type result struct {
		session SessionState
		err     error
	}

	done := make(chan result, 1)

	go func() {
		s, err := run()
		done <- result{session: s, err: err}
	}()

	if snapshot := <-entered; snapshot != oldCfg {
		t.Fatalf("%s phase 2 snapshot = %p, want original generation %p", operation, snapshot, oldCfg)
	}

	if err := sm.applyConfig(newCfg); err != nil {
		t.Fatalf("apply config: %v", err)
	}

	close(release)

	got := <-done
	sm.launchPhase2Hook = nil

	if got.err != nil {
		t.Fatalf("%s after reload: %v", operation, got.err)
	}

	return got.session
}

func assertOldLifecycleGeneration(t *testing.T, sm *SessionManager, sess SessionState, oldCfg *config.Config, recordPath, baseArg string) {
	t.Helper()

	argv := waitForRecordedArgv(t, recordPath, "old-hook-SessionStart")
	wants := []string{baseArg, "old-hook-SessionStart", "old-hook-trust", "old-mcp-graith"}

	for _, want := range wants {
		found := false

		for _, arg := range argv {
			if arg == want {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("launch argv missing old-generation marker %q: %v", want, argv)
		}
	}

	haveOldPrompt := false

	for _, arg := range argv {
		if strings.HasPrefix(arg, "old-prompt-") {
			haveOldPrompt = true
		}

		if strings.HasPrefix(arg, "new-") {
			t.Errorf("launch mixed in new-generation arg %q: %v", arg, argv)
		}
	}

	if !haveOldPrompt {
		t.Errorf("launch argv missing old-generation prompt adapter: %v", argv)
	}

	if sess.CreationCfg == nil {
		t.Fatal("CreationCfg is nil")
	}

	if want := oldCfg.Agents["thrawn"]; !reflect.DeepEqual(sess.CreationCfg.Agent, want) {
		t.Errorf("CreationCfg.Agent came from another generation\n got: %#v\nwant: %#v", sess.CreationCfg.Agent, want)
	}

	if want := oldCfg.Sandbox.Merge(oldCfg.Agents["thrawn"].Sandbox); !reflect.DeepEqual(sess.CreationCfg.SandboxConfig, want) {
		t.Errorf("CreationCfg.SandboxConfig came from another generation\n got: %#v\nwant: %#v", sess.CreationCfg.SandboxConfig, want)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, sess.ID) })
}

func TestLifecycleLaunchUsesOneConfigGenerationAcrossReload(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		recordPath := filepath.Join(t.TempDir(), "create.argv")
		oldCfg := lifecycleGenerationConfig(t, recordPath, "old")
		newCfg := lifecycleGenerationConfig(t, recordPath, "new")
		sm := newSMWithConfig(t, oldCfg)

		created := runPausedLifecycle(t, sm, "create", oldCfg, newCfg, func() (SessionState, error) {
			return sm.Create(CreateOpts{
				Name: "braw-create", AgentName: "thrawn", NoRepo: true, AgentHooks: true, Rows: 24, Cols: 80,
			})
		})
		assertOldLifecycleGeneration(t, sm, created, oldCfg, recordPath, "old-base")
	})

	t.Run("resume", func(t *testing.T) {
		recordPath := filepath.Join(t.TempDir(), "resume.argv")
		oldCfg := lifecycleGenerationConfig(t, recordPath, "old")
		newCfg := lifecycleGenerationConfig(t, recordPath, "new")
		sm := newSMWithConfig(t, oldCfg)

		created, err := sm.Create(CreateOpts{
			Name: "braw-resume", AgentName: "thrawn", NoRepo: true, AgentHooks: true, Rows: 24, Cols: 80,
		})
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })
		waitForRecordedArgv(t, recordPath, "old-hook-SessionStart")

		if err := sm.Stop(created.ID); err != nil {
			t.Fatal(err)
		}

		waitForStatus(t, sm, created.ID, StatusStopped)

		if err := os.Remove(recordPath); err != nil {
			t.Fatal(err)
		}

		resumed := runPausedLifecycle(t, sm, "resume", oldCfg, newCfg, func() (SessionState, error) {
			return sm.Resume(created.ID, 24, 80)
		})
		assertOldLifecycleGeneration(t, sm, resumed, oldCfg, recordPath, "old-resume")
	})

	t.Run("fork", func(t *testing.T) {
		recordPath := filepath.Join(t.TempDir(), "fork.argv")
		oldCfg := lifecycleGenerationConfig(t, recordPath, "old")
		newCfg := lifecycleGenerationConfig(t, recordPath, "new")
		sm := newSMWithConfig(t, oldCfg)
		repo := initTempGitRepo(t)

		source, err := sm.Create(CreateOpts{
			Name: "braw-source", AgentName: "thrawn", RepoPath: repo, BaseBranch: "main", NoFetch: true,
			AgentHooks: true, Rows: 24, Cols: 80,
		})
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { stopAndClosePTY(sm, source.ID) })
		waitForRecordedArgv(t, recordPath, "old-hook-SessionStart")

		if err := os.Remove(recordPath); err != nil {
			t.Fatal(err)
		}

		forked := runPausedLifecycle(t, sm, "fork", oldCfg, newCfg, func() (SessionState, error) {
			return sm.Fork("braw-fork", source.ID, 24, 80)
		})
		assertOldLifecycleGeneration(t, sm, forked, oldCfg, recordPath, "old-fork")
	})
}
