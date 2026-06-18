# In-Place Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow graith sessions to run directly in a repo directory without creating a git worktree, controlled by a `[[repos]]` config section so agents cannot abuse it to get write access to arbitrary directories.

**Architecture:** A new `[[repos]]` config section declares repos eligible for in-place mode. The CLI gains `--in-place` and `--allow-concurrent` flags. The daemon validates in-place requests against config entries, skips worktree/branch creation, and on deletion only removes graith state (never touches the repo). The `InPlace` bool propagates through protocol, state, and display layers.

**Tech Stack:** Go, TOML config (go-toml/v2), Cobra CLI, bubbletea overlay

**Review findings (Claude + Codex):**
- `[[repos]]` is a graith launch policy, not an OS-level sandbox. Document that unsandboxed agents can still write anywhere the user can.
- Resume must re-validate against `[[repos]]` config (repo could have been removed).
- Resume must also check the concurrent session guard (stop A, create B, resume A = two agents).
- Flag combinations must be validated: reject `--in-place --no-repo`, `--in-place --share-worktree`, `--in-place --base`.
- `cleanupOnError` in Create must not call `TeardownSession` for in-place sessions.
- Skip `DiscoverDefaultBranch` for in-place (target repos may have no remote).
- Leave `BaseBranch` empty so unpushed/dirty git detection is skipped gracefully.
- Set `GRAITH_IN_PLACE=true` env var for agents/hooks.
- Delete CLI (`delete.go`) dirty/unpushed warning should be skipped or reworded for in-place.
- Fork rejection is correct for v1; future work could fork in-place into a worktree.

---

### Task 1: Add `[[repos]]` config section

**Files:**
- Modify: `internal/config/config.go:15-28` (Config struct)
- Modify: `internal/config/config.go:265-305` (Default function)
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test for `[[repos]]` parsing**

```go
func TestLoadConfigRepos(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[[repos]]
path = "~/Code/devenv"

[[repos]]
path = "~/Code/scripts"
allow_concurrent = true
`
	os.WriteFile(cfgPath, []byte(toml), 0o644)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("Repos = %d entries, want 2", len(cfg.Repos))
	}
	if cfg.Repos[0].Path != "~/Code/devenv" {
		t.Errorf("Repos[0].Path = %q, want ~/Code/devenv", cfg.Repos[0].Path)
	}
	if cfg.Repos[0].AllowConcurrent {
		t.Error("Repos[0].AllowConcurrent = true, want false (default)")
	}
	if !cfg.Repos[1].AllowConcurrent {
		t.Error("Repos[1].AllowConcurrent = false, want true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadConfigRepos -v`
Expected: FAIL — `cfg.Repos` doesn't exist

- [ ] **Step 3: Add the RepoConfig struct and Repos field to Config**

In `internal/config/config.go`, add the struct and field:

```go
type RepoConfig struct {
	Path            string `toml:"path"`
	AllowConcurrent bool   `toml:"allow_concurrent"`
}
```

Add to the `Config` struct after `AllowedRepoPaths`:

```go
Repos            []RepoConfig     `toml:"repos"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestLoadConfigRepos -v`
Expected: PASS

- [ ] **Step 5: Write the failing test for `FindRepo` lookup method**

```go
func TestFindRepo(t *testing.T) {
	home, _ := os.UserHomeDir()

	t.Run("exact match", func(t *testing.T) {
		cfg := &Config{Repos: []RepoConfig{{Path: "~/Code/devenv"}}}
		rc, ok := cfg.FindRepo(filepath.Join(home, "Code", "devenv"))
		if !ok {
			t.Fatal("expected to find repo")
		}
		if rc.Path != "~/Code/devenv" {
			t.Errorf("Path = %q, want ~/Code/devenv", rc.Path)
		}
	})

	t.Run("no match", func(t *testing.T) {
		cfg := &Config{Repos: []RepoConfig{{Path: "~/Code/devenv"}}}
		_, ok := cfg.FindRepo("/tmp/random")
		if ok {
			t.Error("expected no match for /tmp/random")
		}
	})

	t.Run("symlink resolved", func(t *testing.T) {
		actual := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(actual, link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
		cfg := &Config{Repos: []RepoConfig{{Path: actual}}}
		_, ok := cfg.FindRepo(link)
		if !ok {
			t.Error("expected symlink to resolve and match")
		}
	})
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestFindRepo -v`
Expected: FAIL — `FindRepo` doesn't exist

- [ ] **Step 7: Implement `FindRepo` on Config**

```go
func (c *Config) FindRepo(repoPath string) (RepoConfig, bool) {
	repoPath = ResolvePath(repoPath)
	for _, r := range c.Repos {
		if ResolvePath(r.Path) == repoPath {
			return r, true
		}
	}
	return RepoConfig{}, false
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/config/ -run "TestFindRepo|TestLoadConfigRepos" -v`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add [[repos]] config section for in-place sessions"
```

---

### Task 2: Add `InPlace` and `AllowConcurrent` to protocol and state

**Files:**
- Modify: `internal/protocol/messages.go:44-53` (CreateMsg)
- Modify: `internal/protocol/messages.go:168-191` (SessionInfo)
- Modify: `internal/daemon/state.go:26-53` (SessionState)

- [ ] **Step 1: Add fields to `CreateMsg`**

In `internal/protocol/messages.go`, add to `CreateMsg`:

```go
InPlace        bool   `json:"in_place,omitempty"`
AllowConcurrent bool  `json:"allow_concurrent,omitempty"`
```

- [ ] **Step 2: Add `InPlace` to `SessionInfo`**

In `internal/protocol/messages.go`, add to `SessionInfo` (after `SharedWorktree`):

```go
InPlace        bool     `json:"in_place,omitempty"`
```

- [ ] **Step 3: Add `InPlace` to `SessionState`**

In `internal/daemon/state.go`, add to `SessionState` (after `SharedWorktree`):

```go
InPlace            bool                  `json:"in_place,omitempty"`
```

- [ ] **Step 4: Add `InPlace` to `toSessionInfo` mapping**

In `internal/daemon/handler.go`, in the `toSessionInfo` function (~line 626), add:

```go
InPlace:        s.InPlace,
```

- [ ] **Step 5: Run existing tests to verify no regressions**

Run: `go test ./internal/protocol/ ./internal/daemon/ -v`
Expected: PASS (new fields are omitempty, so zero values change nothing)

- [ ] **Step 6: Commit**

```bash
git add internal/protocol/messages.go internal/daemon/state.go internal/daemon/handler.go
git commit -m "feat: add InPlace field to protocol, state, and session info"
```

---

### Task 3: Add `--in-place` and `--allow-concurrent` CLI flags

**Files:**
- Modify: `internal/cli/new.go`

- [ ] **Step 1: Add flag variables and wire them to CreateMsg**

Add new variables alongside existing ones (~line 13):

```go
newInPlace        bool
newAllowConcurrent bool
```

Add to the `CreateMsg` construction inside `RunE` (~line 59):

```go
InPlace:        newInPlace,
AllowConcurrent: newAllowConcurrent,
```

Add flag registrations in `init()` (~line 110):

```go
newCmd.Flags().BoolVar(&newInPlace, "in-place", false, "run agent directly in the repo without creating a worktree")
newCmd.Flags().BoolVar(&newAllowConcurrent, "allow-concurrent", false, "allow multiple in-place sessions on the same repo")
```

- [ ] **Step 2: Add client-side validation for flag combinations**

In the `RunE` function, before connecting to the daemon, add:

```go
if newAllowConcurrent && !newInPlace {
	return fmt.Errorf("--allow-concurrent requires --in-place")
}
if newInPlace && newNoRepo {
	return fmt.Errorf("--in-place and --no-repo are mutually exclusive")
}
if newInPlace && newShareWorktree != "" {
	return fmt.Errorf("--in-place and --share-worktree are mutually exclusive")
}
if newInPlace && newBase != "" {
	return fmt.Errorf("--in-place and --base are mutually exclusive (in-place sessions don't create branches)")
}
```

- [ ] **Step 3: Run build to verify**

Run: `go build ./cmd/graith`
Expected: builds successfully

- [ ] **Step 4: Commit**

```bash
git add internal/cli/new.go
git commit -m "feat: add --in-place and --allow-concurrent flags to gr new"
```

---

### Task 4: Implement in-place session creation in the daemon

**Files:**
- Modify: `internal/daemon/daemon.go:264-509` (Create method)
- Modify: `internal/daemon/handler.go:89-104` (handler passes new fields)
- Test: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write the failing test for in-place session creation**

```go
func TestCreateInPlace(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(repoDir, 0o755)
	initBareGitRepo(t, repoDir)

	sm := newTestSessionManager(t, dir)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sess, err := sm.Create("test", "echo", repoDir, "", "", false, "", false, false, true, false, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.InPlace {
		t.Error("InPlace = false, want true")
	}
	if sess.WorktreePath != repoDir {
		t.Errorf("WorktreePath = %q, want %q", sess.WorktreePath, repoDir)
	}
	if sess.Branch != "" {
		t.Errorf("Branch = %q, want empty", sess.Branch)
	}
}
```

Note: the exact `Create` signature will change — the two new booleans (`inPlace`, `allowConcurrent`) need to be added. Look at the current signature and insert them. The test helper `newTestSessionManager` already exists in daemon_test.go; reuse it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestCreateInPlace -v`
Expected: FAIL — wrong number of arguments to Create

- [ ] **Step 3: Update the `Create` method signature**

In `internal/daemon/daemon.go`, update the `Create` signature to add `inPlace` and `allowConcurrent` parameters:

```go
func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch, prompt string, noRepo bool, shareWorktree string, agentHooks bool, inPlace bool, allowConcurrent bool, rows, cols uint16) (SessionState, error) {
```

- [ ] **Step 4: Update the handler to pass new fields from CreateMsg**

In `internal/daemon/handler.go` (~line 99), update the call:

```go
sess, err := sm.Create(c.Name, agentName, c.RepoPath, c.Base, c.Prompt, c.NoRepo, c.ShareWorktree, c.AgentHooks, c.InPlace, c.AllowConcurrent, clientRows, clientCols)
```

- [ ] **Step 5: Add the in-place case to the switch in Create**

In `internal/daemon/daemon.go`, add a new case in the `switch` block (~line 278), after `case noRepo:` and before `default:`. The in-place case must come before the default worktree case:

```go
case inPlace:
	rc, ok := sm.cfg.FindRepo(repoPath)
	if !ok {
		return SessionState{}, fmt.Errorf("repo path %q is not configured in [[repos]] — add it to config to use --in-place", repoPath)
	}

	if !allowConcurrent && !rc.AllowConcurrent {
		for _, s := range sm.state.Sessions {
			if s.InPlace && s.WorktreePath == config.ResolvePath(repoPath) && s.Status == StatusRunning {
				return SessionState{}, fmt.Errorf("an in-place session %q is already running in %q — use --allow-concurrent to override", s.Name, repoPath)
			}
		}
	}

	if !git.IsInsideGitRepo(repoPath) {
		return SessionState{}, fmt.Errorf("not inside a git repository: %s", repoPath)
	}

	var err error
	repoRoot, err = git.RepoRootPath(repoPath)
	if err != nil {
		return SessionState{}, fmt.Errorf("find repo root: %w", err)
	}

	repoName = filepath.Base(repoRoot)
	worktreePath = repoRoot
```

- [ ] **Step 5b: Add daemon-side flag combination validation**

At the top of the `Create` method, before the switch, add:

```go
if inPlace && noRepo {
	return SessionState{}, fmt.Errorf("--in-place and --no-repo are mutually exclusive")
}
if inPlace && shareWorktree != "" {
	return SessionState{}, fmt.Errorf("--in-place and --share-worktree are mutually exclusive")
}
```

- [ ] **Step 5c: Set `GRAITH_IN_PLACE` env var**

In the env map construction (~line 389), add after the existing env vars:

```go
if inPlace {
	env["GRAITH_IN_PLACE"] = "true"
}
```

- [ ] **Step 6: Update the `cleanupOnError` closure to handle in-place**

Update the closure (~line 366) to skip cleanup for in-place sessions (no worktree or branch was created):

```go
cleanupOnError := func() {
	sm.cleanupHooks(id)
	if sharedWorktree || inPlace {
		return
	}
	if noRepo {
		os.RemoveAll(worktreePath)
	} else {
		_ = git.TeardownSession(repoRoot, worktreePath, branchName)
	}
}
```

- [ ] **Step 7: Set `InPlace` on the SessionState**

In the `sessState` construction (~line 470), add:

```go
InPlace:        inPlace,
```

- [ ] **Step 8: Fix all other callers of Create to pass the new parameters**

Search for all calls to `sm.Create(` and add `false, false,` for `inPlace` and `allowConcurrent`. There is at least one in `internal/daemon/daemon_test.go` and likely in integration tests. Run:

```bash
grep -rn 'sm\.Create(' internal/ --include='*.go'
```

Update each call to include the two new `false, false` arguments before `rows, cols`.

Also check `internal/mcp/server.go` for an MCP create-session handler that calls `Create`.

- [ ] **Step 9: Run tests to verify**

Run: `go test ./internal/daemon/ -run TestCreateInPlace -v`
Expected: PASS

Run: `go test ./internal/daemon/ -v`
Expected: all PASS (existing tests still work with the new parameters)

- [ ] **Step 10: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/handler.go internal/daemon/daemon_test.go internal/mcp/server.go
git commit -m "feat: implement in-place session creation in daemon"
```

---

### Task 5: In-place session deletion skips git cleanup

**Files:**
- Modify: `internal/daemon/daemon.go:868-931` (Delete method)
- Test: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestDeleteInPlaceLeavesRepoUntouched(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(repoDir, 0o755)
	initBareGitRepo(t, repoDir)

	marker := filepath.Join(repoDir, "important.txt")
	os.WriteFile(marker, []byte("keep me"), 0o644)

	sm := newTestSessionManager(t, dir)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sess, err := sm.Create("inplace-del", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err != nil {
		t.Fatal(err)
	}

	if err := sm.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("marker file should still exist after in-place deletion: %v", err)
	}
	if _, err := os.Stat(repoDir); err != nil {
		t.Errorf("repo dir should still exist after in-place deletion: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails (or passes if delete already handles it)**

Run: `go test ./internal/daemon/ -run TestDeleteInPlaceLeavesRepoUntouched -v`

The current delete logic (~line 914) has:
```go
case repoPath != "":
    _ = git.TeardownSession(repoPath, worktreePath, branch)
```

Since in-place sets `repoPath` but not `branch`, `TeardownSession` will be called but with empty branch. Check whether this is safe or needs guarding.

- [ ] **Step 3: Update Delete to handle in-place sessions**

In `internal/daemon/daemon.go`, in the `Delete` method, capture the `InPlace` field alongside the existing fields (~line 893):

```go
inPlace := sessState.InPlace
```

Then update the cleanup switch (~line 914):

```go
switch {
case shared:
	scratchDir := filepath.Join(sm.paths.DataDir, "scratch", id)
	_ = os.RemoveAll(scratchDir)
case inPlace:
	// In-place sessions: leave the repo completely untouched
case repoPath != "":
	_ = git.TeardownSession(repoPath, worktreePath, branch)
case worktreePath != "":
	_ = os.RemoveAll(worktreePath)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/daemon/ -run TestDeleteInPlaceLeavesRepoUntouched -v`
Expected: PASS

Run: `go test ./internal/daemon/ -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat: in-place session deletion skips git cleanup"
```

---

### Task 6: Concurrent session guard

**Files:**
- Test: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write test for concurrent rejection**

```go
func TestCreateInPlaceRejectsConcurrent(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(repoDir, 0o755)
	initBareGitRepo(t, repoDir)

	sm := newTestSessionManager(t, dir)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	_, err := sm.Create("first", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err != nil {
		t.Fatal(err)
	}

	_, err = sm.Create("second", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err == nil {
		t.Fatal("expected error for concurrent in-place session")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %q, want mention of already running", err.Error())
	}
}
```

- [ ] **Step 2: Write test for --allow-concurrent override**

```go
func TestCreateInPlaceAllowConcurrent(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(repoDir, 0o755)
	initBareGitRepo(t, repoDir)

	sm := newTestSessionManager(t, dir)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	_, err := sm.Create("first", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err != nil {
		t.Fatal(err)
	}

	_, err = sm.Create("second", "echo", repoDir, "", "", false, "", false, true, true, 24, 80)
	if err != nil {
		t.Fatalf("--allow-concurrent should permit second session: %v", err)
	}
}
```

- [ ] **Step 3: Write test for config-level allow_concurrent**

```go
func TestCreateInPlaceConfigAllowConcurrent(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(repoDir, 0o755)
	initBareGitRepo(t, repoDir)

	sm := newTestSessionManager(t, dir)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, AllowConcurrent: true}}

	_, err := sm.Create("first", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err != nil {
		t.Fatal(err)
	}

	_, err = sm.Create("second", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err != nil {
		t.Fatalf("config allow_concurrent should permit second session: %v", err)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/daemon/ -run "TestCreateInPlaceRejectsConcurrent|TestCreateInPlaceAllowConcurrent|TestCreateInPlaceConfigAllowConcurrent" -v`
Expected: PASS (the logic was already added in Task 4, Step 5)

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon_test.go
git commit -m "test: concurrent in-place session guards"
```

---

### Task 7: Reject --in-place when repo not in [[repos]] config

**Files:**
- Test: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write test for rejection**

```go
func TestCreateInPlaceRejectsUnconfiguredRepo(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(repoDir, 0o755)
	initBareGitRepo(t, repoDir)

	sm := newTestSessionManager(t, dir)
	// No Repos configured

	_, err := sm.Create("test", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err == nil {
		t.Fatal("expected error for unconfigured repo")
	}
	if !strings.Contains(err.Error(), "not configured in [[repos]]") {
		t.Errorf("error = %q, want mention of [[repos]]", err.Error())
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/daemon/ -run TestCreateInPlaceRejectsUnconfiguredRepo -v`
Expected: PASS (the validation was added in Task 4, Step 5)

- [ ] **Step 3: Commit**

```bash
git add internal/daemon/daemon_test.go
git commit -m "test: in-place rejects unconfigured repo paths"
```

---

### Task 8: Display in-place sessions in list and overlay

**Files:**
- Modify: `internal/cli/list.go`
- Modify: `internal/client/overlay.go`

- [ ] **Step 1: Update list output**

In `internal/cli/list.go`, find where `branch` is displayed (~line 123). When `s.InPlace` is true and `s.Branch` is empty, display "(in-place)" instead of a branch name:

```go
branch := s.Branch
if branch != "" {
	parts := strings.SplitN(branch, "/", 3)
	if len(parts) == 3 {
		branch = parts[2]
	}
} else if s.InPlace {
	branch = "(in-place)"
}
```

- [ ] **Step 2: Update overlay display**

In `internal/client/overlay.go`, find the `displayBranch` function (~line 78). Update it to handle empty branch for in-place sessions. In the `renderItem` method where `branchVal` is assigned (~line 302), add a fallback:

```go
branchVal := displayBranch(si.info.Branch, si.info.Name)
if branchVal == "" && si.info.InPlace {
	branchVal = "(in-place)"
}
```

Also update the detail pane (~line 771) similarly:

```go
if s.Branch != "" {
	branch := s.Branch
	if p := strings.SplitN(branch, "/", 3); len(p) == 3 {
		branch = p[2]
	}
	// ... render branch
} else if s.InPlace {
	// render "(in-place)" in the branch column
}
```

- [ ] **Step 3: Run build**

Run: `go build ./cmd/graith`
Expected: builds successfully

- [ ] **Step 4: Commit**

```bash
git add internal/cli/list.go internal/client/overlay.go
git commit -m "feat: display in-place indicator in list and overlay"
```

---

### Task 9: Wire in-place through MCP server

**Files:**
- Modify: `internal/mcp/server.go`

- [ ] **Step 1: Check MCP server create-session handler**

The MCP server has a create-session tool that constructs a `CreateMsg`. Find the input struct and add `InPlace` and `AllowConcurrent` fields:

```go
InPlace        bool   `json:"in_place,omitempty" jsonschema:"description=Run agent directly in repo without creating a worktree"`
AllowConcurrent bool  `json:"allow_concurrent,omitempty" jsonschema:"description=Allow multiple in-place sessions on same repo"`
```

Wire them through to the `Create` call.

- [ ] **Step 2: Run build and tests**

Run: `go build ./cmd/graith && go test ./internal/mcp/ -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/mcp/server.go
git commit -m "feat: expose in-place options in MCP create-session tool"
```

---

### Task 10: Fork rejects in-place sessions

**Files:**
- Modify: `internal/daemon/daemon.go:511-687` (Fork method)
- Test: `internal/daemon/daemon_test.go`

Forking an in-place session doesn't make sense — there's no worktree to branch from and the whole point of in-place is to avoid branching.

- [ ] **Step 1: Write the failing test**

```go
func TestForkInPlaceRejects(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(repoDir, 0o755)
	initBareGitRepo(t, repoDir)

	sm := newTestSessionManager(t, dir)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sess, err := sm.Create("inplace-fork", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err != nil {
		t.Fatal(err)
	}

	_, err = sm.Fork("forked", sess.ID, 24, 80)
	if err == nil {
		t.Fatal("expected error forking an in-place session")
	}
	if !strings.Contains(err.Error(), "in-place") {
		t.Errorf("error = %q, want mention of in-place", err.Error())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestForkInPlaceRejects -v`
Expected: FAIL (Fork currently only checks for empty RepoPath)

- [ ] **Step 3: Add in-place guard to Fork**

In `internal/daemon/daemon.go`, in the `Fork` method, after the existing `source.RepoPath == ""` check (~line 523), add:

```go
if source.InPlace {
	return SessionState{}, fmt.Errorf("cannot fork session %q: in-place sessions cannot be forked", source.Name)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/daemon/ -run TestForkInPlaceRejects -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat: reject forking in-place sessions"
```

---

### Task 11: Resume re-validates in-place sessions

**Files:**
- Modify: `internal/daemon/daemon.go:722-866` (Resume method)
- Test: `internal/daemon/daemon_test.go`

Resume must re-validate two things for in-place sessions: (1) the repo is still in `[[repos]]` config, and (2) no other in-place session for the same repo is running.

- [ ] **Step 1: Write the failing test for config removal**

```go
func TestResumeInPlaceRejectsRemovedConfig(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(repoDir, 0o755)
	initBareGitRepo(t, repoDir)

	sm := newTestSessionManager(t, dir)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sess, err := sm.Create("test", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	waitForExit(t, sm, sess.ID)

	sm.cfg.Repos = nil

	_, err = sm.Resume(sess.ID, 24, 80)
	if err == nil {
		t.Fatal("expected error resuming in-place session after repo removed from config")
	}
	if !strings.Contains(err.Error(), "[[repos]]") {
		t.Errorf("error = %q, want mention of [[repos]]", err.Error())
	}
}
```

- [ ] **Step 2: Write the failing test for concurrent resume**

```go
func TestResumeInPlaceRejectsConcurrentRunning(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(repoDir, 0o755)
	initBareGitRepo(t, repoDir)

	sm := newTestSessionManager(t, dir)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, AllowConcurrent: false}}

	sessA, err := sm.Create("first", "echo", repoDir, "", "", false, "", false, true, false, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	waitForExit(t, sm, sessA.ID)

	_, err = sm.Create("second", "echo", repoDir, "", "", false, "", false, true, true, 24, 80)
	if err != nil {
		t.Fatal(err)
	}

	_, err = sm.Resume(sessA.ID, 24, 80)
	if err == nil {
		t.Fatal("expected error: another in-place session is running in the same repo")
	}
}
```

- [ ] **Step 3: Add re-validation to Resume**

In `internal/daemon/daemon.go`, in the `Resume` method, after the `sessState.SharedWorktree && !sessState.Sandboxed` check (~line 777), add:

```go
if sessState.InPlace {
	rc, ok := sm.cfg.FindRepo(sessState.WorktreePath)
	if !ok {
		return SessionState{}, fmt.Errorf("repo path %q is no longer configured in [[repos]] — add it back to config to resume this in-place session", sessState.WorktreePath)
	}
	if !rc.AllowConcurrent {
		for _, s := range sm.state.Sessions {
			if s.ID != id && s.InPlace && s.WorktreePath == sessState.WorktreePath && s.Status == StatusRunning {
				return SessionState{}, fmt.Errorf("another in-place session %q is already running in %q — stop it first or use allow_concurrent in config", s.Name, sessState.WorktreePath)
			}
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/daemon/ -run "TestResumeInPlace" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat: resume re-validates in-place sessions against config and concurrent guard"
```

---

### Task 12: Delete CLI skips dirty/unpushed warning for in-place

**Files:**
- Modify: `internal/cli/delete.go`

- [ ] **Step 1: Update delete confirmation to handle in-place**

In `internal/cli/delete.go`, find where dirty/unpushed work is checked before deletion (~line 45). Add a guard that skips or rewords the warning when `s.InPlace` is true:

```go
if !s.InPlace && (s.Dirty || s.UnpushedCount > 0) {
	// existing dirty/unpushed warning logic
}
```

For in-place sessions, the repo is not managed by graith, so the warning about losing work is misleading — deletion only removes graith state.

- [ ] **Step 2: Run build**

Run: `go build ./cmd/graith`
Expected: builds successfully

- [ ] **Step 3: Commit**

```bash
git add internal/cli/delete.go
git commit -m "feat: skip dirty/unpushed warning for in-place session deletion"
```

---

### Task 13: State persistence round-trip test

**Files:**
- Test: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write the test**

```go
func TestStateSaveLoadInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	state := &State{
		Version: CurrentStateVersion,
		Sessions: map[string]*SessionState{
			"abc123": {
				ID: "abc123", Name: "inplace-test", Agent: "claude",
				WorktreePath: "/some/repo", InPlace: true,
				Status: StatusRunning, CreatedAt: time.Now().UTC(),
			},
		},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	s := loaded.Sessions["abc123"]
	if !s.InPlace {
		t.Error("InPlace not preserved across save/load")
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/daemon/ -run TestStateSaveLoadInPlace -v`
Expected: PASS (JSON serialization handles bool fields automatically)

- [ ] **Step 3: Commit**

```bash
git add internal/daemon/daemon_test.go
git commit -m "test: verify InPlace field persists across state save/load"
```

---

### Task 14: Full build and test sweep

- [ ] **Step 1: Run gofmt**

Run: `gofmt -w ./internal/config/ ./internal/daemon/ ./internal/protocol/ ./internal/cli/ ./internal/client/ ./internal/mcp/`

- [ ] **Step 2: Run vet**

Run: `go vet ./...`
Expected: no errors

- [ ] **Step 3: Run full test suite with race detector**

Run: `go test -race ./...`
Expected: all PASS

- [ ] **Step 4: Build the binary**

Run: `go build ./cmd/graith`
Expected: builds successfully

- [ ] **Step 5: Verify --help shows new flags**

Run: `./graith new --help`
Expected: output includes `--in-place` and `--allow-concurrent` flags with descriptions

- [ ] **Step 6: Commit any formatting fixes**

```bash
git add -A
git commit -m "chore: gofmt and final cleanup"
```
