# Agent-Friendly Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make graith automatically switch to JSON output when running inside an agent, add `gr stop --children` with env auto-resolve, and add `gr msg send --children/--parent` for easier parent-child communication.

**Architecture:** Three independent features that share a common pattern: detecting the agent environment from `GRAITH_SESSION_ID` and other env vars. Feature 1 adds a new `internal/agent` package for detection logic and wires it into the CLI root. Feature 2 extends the stop protocol/daemon/CLI to mirror existing delete-with-children. Feature 3 adds target resolution flags to `msg send`.

**Tech Stack:** Go, Cobra CLI, graith protocol (JSON control messages over Unix socket)

---

### Task 1: Agent Detection Package

**Files:**
- Create: `internal/agent/agent.go`
- Create: `internal/agent/agent_test.go`

- [ ] **Step 1: Write the tests for agent detection**

```go
// internal/agent/agent_test.go
package agent

import (
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{
			name: "no env vars",
			env:  nil,
			want: false,
		},
		{
			name: "GR_AGENT_MODE=1 enables",
			env:  map[string]string{"GR_AGENT_MODE": "1"},
			want: true,
		},
		{
			name: "GR_AGENT_MODE=true enables",
			env:  map[string]string{"GR_AGENT_MODE": "true"},
			want: true,
		},
		{
			name: "GR_AGENT_MODE=yes enables case-insensitive",
			env:  map[string]string{"GR_AGENT_MODE": "YES"},
			want: true,
		},
		{
			name: "GR_AGENT_MODE=0 disables even with other vars",
			env:  map[string]string{"GR_AGENT_MODE": "0", "GRAITH_SESSION_ID": "abc"},
			want: false,
		},
		{
			name: "GR_AGENT_MODE=false disables",
			env:  map[string]string{"GR_AGENT_MODE": "false"},
			want: false,
		},
		{
			name: "GR_AGENT_MODE=no disables",
			env:  map[string]string{"GR_AGENT_MODE": "NO"},
			want: false,
		},
		{
			name: "GR_AGENT_MODE=invalid treated as not set",
			env:  map[string]string{"GR_AGENT_MODE": "maybe"},
			want: false,
		},
		{
			name: "GRAITH_SESSION_ID enables",
			env:  map[string]string{"GRAITH_SESSION_ID": "sess-123"},
			want: true,
		},
		{
			name: "CLAUDECODE enables",
			env:  map[string]string{"CLAUDECODE": "1"},
			want: true,
		},
		{
			name: "CLAUDE_CODE enables",
			env:  map[string]string{"CLAUDE_CODE": "1"},
			want: true,
		},
		{
			name: "CURSOR_AGENT enables",
			env:  map[string]string{"CURSOR_AGENT": "1"},
			want: true,
		},
		{
			name: "GITHUB_COPILOT enables",
			env:  map[string]string{"GITHUB_COPILOT": "1"},
			want: true,
		},
		{
			name: "AMAZON_Q enables",
			env:  map[string]string{"AMAZON_Q": "1"},
			want: true,
		},
		{
			name: "OPENCODE enables",
			env:  map[string]string{"OPENCODE": "1"},
			want: true,
		},
		{
			name: "GR_AGENT_MODE=0 overrides CLAUDECODE",
			env:  map[string]string{"GR_AGENT_MODE": "0", "CLAUDECODE": "1"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				v, ok := tt.env[key]
				return v, ok
			}
			got := detect(lookup)
			if got != tt.want {
				t.Errorf("detect() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agent/ -v -run TestDetect`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement agent detection**

```go
// internal/agent/agent.go
package agent

import (
	"os"
	"strings"
)

var agentDetected bool

func init() {
	agentDetected = detect(os.LookupEnv)
}

// Detected returns true if the process is running inside an AI agent environment.
func Detected() bool {
	return agentDetected
}

var agentEnvVars = []string{
	"GRAITH_SESSION_ID",
	"CLAUDECODE",
	"CLAUDE_CODE",
	"CURSOR_AGENT",
	"GITHUB_COPILOT",
	"AMAZON_Q",
	"OPENCODE",
}

func detect(lookupEnv func(string) (string, bool)) bool {
	if v, ok := lookupEnv("GR_AGENT_MODE"); ok {
		switch strings.ToLower(v) {
		case "1", "true", "yes":
			return true
		case "0", "false", "no":
			return false
		}
		// Invalid value — fall through to auto-detection
	}

	for _, key := range agentEnvVars {
		if _, ok := lookupEnv(key); ok {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/agent/ -v -run TestDetect`
Expected: PASS — all cases

- [ ] **Step 5: Commit**

```bash
git add internal/agent/agent.go internal/agent/agent_test.go
git commit -m "feat: add agent detection package

Detects when gr is running inside an AI agent environment via
env vars (GR_AGENT_MODE, GRAITH_SESSION_ID, CLAUDECODE, etc.).
Exposes agent.Detected() for use in CLI auto-JSON."
```

---

### Task 2: Wire Agent Detection into CLI Root

**Files:**
- Modify: `internal/cli/root.go`

- [ ] **Step 1: Add `--agent-mode` flag and auto-JSON logic**

In `internal/cli/root.go`, add the import, the flag variable, the flag
registration, and the auto-enable logic.

Add to the `var` block at the top:

```go
agentMode bool
```

In `PersistentPreRunE`, after `out = output.New(jsonOutput)`, add:

```go
if !jsonOutput && (agentMode || agent.Detected()) {
    jsonOutput = true
    out = output.New(jsonOutput)
}
```

In `executeWithArgs`, update the error fallback block (the `if w == nil` branch)
to also check agent detection:

```go
if w == nil {
    rootCmd.PersistentFlags().Parse(args)
    if !jsonOutput && (agentMode || agent.Detected()) {
        jsonOutput = true
    }
    w = output.New(jsonOutput)
}
```

In `init()`, add the flag:

```go
rootCmd.PersistentFlags().BoolVar(&agentMode, "agent-mode", false, "force agent mode (auto-enables JSON output)")
```

Add the import for `"github.com/d0ugal/graith/internal/agent"`.

- [ ] **Step 2: Run existing tests**

Run: `go test ./internal/cli/ -v -run TestUnknownSubcommand`
Expected: PASS — existing error handling still works

- [ ] **Step 3: Format and vet**

Run: `gofmt -w internal/cli/root.go && go vet ./internal/cli/`
Expected: No output (clean)

- [ ] **Step 4: Commit**

```bash
git add internal/cli/root.go
git commit -m "feat: auto-enable JSON output in agent environments

When running inside a graith session, Claude Code, Cursor, Copilot,
Amazon Q, or OpenCode, gr now automatically outputs JSON. Can be
forced with --agent-mode or GR_AGENT_MODE=1, disabled with
GR_AGENT_MODE=0."
```

---

### Task 3: Protocol — Add Children/ExcludeRoot to StopMsg and DeleteMsg

**Files:**
- Modify: `internal/protocol/messages.go`

- [ ] **Step 1: Add fields to StopMsg**

In `internal/protocol/messages.go`, change `StopMsg` from:

```go
type StopMsg struct {
	SessionID string `json:"session_id"`
}
```

to:

```go
type StopMsg struct {
	SessionID   string `json:"session_id"`
	Children    bool   `json:"children,omitempty"`
	ExcludeRoot bool   `json:"exclude_root,omitempty"`
}
```

- [ ] **Step 2: Add ExcludeRoot to DeleteMsg**

In `internal/protocol/messages.go`, change `DeleteMsg` from:

```go
type DeleteMsg struct {
	SessionID string `json:"session_id"`
	Children  bool   `json:"children,omitempty"`
}
```

to:

```go
type DeleteMsg struct {
	SessionID   string `json:"session_id"`
	Children    bool   `json:"children,omitempty"`
	ExcludeRoot bool   `json:"exclude_root,omitempty"`
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: Success — new fields are optional, so no existing code breaks

- [ ] **Step 4: Commit**

```bash
git add internal/protocol/messages.go
git commit -m "feat: add Children/ExcludeRoot fields to StopMsg and DeleteMsg

Wire-compatible: new optional bool fields default to false,
so existing clients continue to work unchanged."
```

---

### Task 4: Daemon — StopWithChildren Method

**Files:**
- Modify: `internal/daemon/daemon.go`
- Create: `internal/daemon/stopchildren_test.go`

- [ ] **Step 1: Write the test**

```go
// internal/daemon/stopchildren_test.go
package daemon

import (
	"testing"
)

func TestCollectDescendantsIncludesRoot(t *testing.T) {
	sm := &SessionManager{
		state: &State{
			Sessions: map[string]SessionState{
				"root":       {ID: "root", Status: StatusRunning},
				"child1":     {ID: "child1", ParentID: "root", Status: StatusRunning},
				"child2":     {ID: "child2", ParentID: "root", Status: StatusStopped},
				"grandchild": {ID: "grandchild", ParentID: "child1", Status: StatusRunning},
			},
		},
	}

	all := sm.collectDescendants("root")

	found := make(map[string]bool)
	for _, id := range all {
		found[id] = true
	}
	if !found["root"] {
		t.Error("collectDescendants should include root")
	}
	if !found["child1"] || !found["child2"] || !found["grandchild"] {
		t.Error("collectDescendants should include all descendants")
	}
	// Verify leaf-first order: grandchild before child1, child1/child2 before root
	rootIdx := -1
	grandchildIdx := -1
	for i, id := range all {
		if id == "root" {
			rootIdx = i
		}
		if id == "grandchild" {
			grandchildIdx = i
		}
	}
	if grandchildIdx > rootIdx {
		t.Error("grandchild should come before root (leaf-first)")
	}
}

func TestFilterExcludeRoot(t *testing.T) {
	ids := []string{"grandchild", "child1", "child2", "root"}

	filtered := filterExcludeRoot(ids, "root")

	for _, id := range filtered {
		if id == "root" {
			t.Error("filterExcludeRoot should remove root")
		}
	}
	if len(filtered) != 3 {
		t.Errorf("expected 3 items, got %d", len(filtered))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -v -run "TestCollectDescendants|TestFilterExcludeRoot"`
Expected: FAIL — `filterExcludeRoot` not defined

- [ ] **Step 3: Implement StopWithChildren and filterExcludeRoot**

Add to `internal/daemon/daemon.go`, after the existing `Stop` method
(around line 1422):

```go
func filterExcludeRoot(ids []string, rootID string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != rootID {
			result = append(result, id)
		}
	}
	return result
}

// StopWithChildren stops all descendants of rootID. If excludeRoot is true,
// the root session itself is not stopped. Already-stopped sessions are skipped.
// Returns the list of session IDs that were actually stopped.
func (sm *SessionManager) StopWithChildren(rootID string, excludeRoot bool) ([]string, error) {
	sm.mu.Lock()

	if _, ok := sm.state.Sessions[rootID]; !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session %q not found", rootID)
	}

	toStop := sm.collectDescendants(rootID)
	if excludeRoot {
		toStop = filterExcludeRoot(toStop, rootID)
	}

	sm.mu.Unlock()

	var stopped []string
	for _, id := range toStop {
		sm.mu.Lock()
		sess, ok := sm.state.Sessions[id]
		sm.mu.Unlock()
		if !ok {
			continue
		}
		if sess.Status != StatusRunning {
			continue
		}
		ptySess, ok := sm.GetPTY(id)
		if !ok {
			continue
		}
		if err := ptySess.Kill(); err != nil {
			sm.logger.Warn("stop child failed", "session_id", id, "error", err)
			continue
		}
		stopped = append(stopped, id)
	}

	return stopped, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/daemon/ -v -run "TestCollectDescendants|TestFilterExcludeRoot"`
Expected: PASS

- [ ] **Step 5: Run full daemon tests**

Run: `go test ./internal/daemon/ -v`
Expected: PASS — no regressions

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/stopchildren_test.go
git commit -m "feat: add StopWithChildren to SessionManager

Stops all descendants of a session, optionally excluding the root.
Skips already-stopped sessions. Returns the list of IDs that were
actually stopped."
```

---

### Task 5: Handler — Dispatch stop-with-children

**Files:**
- Modify: `internal/daemon/handler.go`

- [ ] **Step 1: Update the stop case in handler dispatch**

In `internal/daemon/handler.go`, replace the `case "stop":` block
(lines 214-226) with:

```go
			case "stop":
				var s protocol.StopMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid stop message"})
					continue
				}
				if s.Children {
					stopped, err := sm.StopWithChildren(s.SessionID, s.ExcludeRoot)
					if err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("stopped", struct {
							SessionID string   `json:"session_id"`
							Stopped   []string `json:"stopped"`
						}{s.SessionID, stopped})
					}
				} else {
					if err := sm.Stop(s.SessionID); err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("stopped", struct {
							SessionID string `json:"session_id"`
						}{s.SessionID})
					}
				}
```

- [ ] **Step 2: Update delete handler to pass ExcludeRoot**

In `internal/daemon/handler.go`, in the `case "delete":` block (line 195),
change:

```go
deleted, err := sm.DeleteWithChildren(d.SessionID)
```

to:

```go
deleted, err := sm.DeleteWithChildren(d.SessionID, d.ExcludeRoot)
```

- [ ] **Step 3: Update DeleteWithChildren signature**

In `internal/daemon/daemon.go`, update the `DeleteWithChildren` method signature
and add root filtering. Change line 1247:

```go
func (sm *SessionManager) DeleteWithChildren(id string) ([]string, error) {
```

to:

```go
func (sm *SessionManager) DeleteWithChildren(id string, excludeRoot bool) ([]string, error) {
```

And after `toDelete := sm.collectDescendants(id)` (line 1255), add:

```go
	if excludeRoot {
		toDelete = filterExcludeRoot(toDelete, id)
	}
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: Success

- [ ] **Step 5: Run daemon tests**

Run: `go test ./internal/daemon/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/handler.go internal/daemon/daemon.go
git commit -m "feat: handle stop-with-children and ExcludeRoot in handler

Dispatches StopWithChildren when Children flag is set on stop message.
Also passes ExcludeRoot through for delete-with-children."
```

---

### Task 6: CLI — `gr stop --children` with env auto-resolve

**Files:**
- Modify: `internal/cli/stop.go`

- [ ] **Step 1: Add `--children` flag and update args/run logic**

Replace the contents of `internal/cli/stop.go` with:

```go
package cli

import (
	"fmt"
	"os"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	stopBatch    batchFlags
	stopChildren bool
)

var stopCmd = &cobra.Command{
	Use:   "stop <name-or-id>",
	Short: "Stop a running session without deleting it",
	Args: func(cmd *cobra.Command, args []string) error {
		if stopChildren && stopBatch.active() {
			return fmt.Errorf("--children cannot be combined with batch filters")
		}
		if stopBatch.active() {
			return cobra.NoArgs(cmd, args)
		}
		if stopChildren {
			return cobra.MaximumNArgs(1)(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if stopBatch.active() {
			return stopBatchRun(cmd)
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		if stopChildren {
			return stopChildrenRun(c, args)
		}

		sessionID, err := resolveSession(c, args[0])
		if err != nil {
			return err
		}

		c.SendControl("stop", protocol.StopMsg{SessionID: sessionID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		out.Print("Session stopped (worktree preserved)\n")
		return nil
	},
}

func stopChildrenRun(c *client.Client, args []string) error {
	var sessionID string
	var excludeRoot bool

	if len(args) == 1 {
		var err error
		sessionID, err = resolveSession(c, args[0])
		if err != nil {
			return err
		}
		excludeRoot = false
	} else {
		sessionID = os.Getenv("GRAITH_SESSION_ID")
		if sessionID == "" {
			return fmt.Errorf("--children with no session arg requires GRAITH_SESSION_ID to be set")
		}
		excludeRoot = true
	}

	c.SendControl("stop", protocol.StopMsg{
		SessionID:   sessionID,
		Children:    true,
		ExcludeRoot: excludeRoot,
	})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}
	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)
		return fmt.Errorf("%s", e.Message)
	}

	var result struct {
		Stopped []string `json:"stopped"`
	}
	protocol.DecodePayload(resp, &result)
	out.Print("Stopped %d sessions\n", len(result.Stopped))
	return nil
}

func stopBatchRun(cmd *cobra.Command) error {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return err
	}

	matched, err := filterSessions(list.Sessions, &stopBatch)
	if err != nil {
		return err
	}
	if len(matched) == 0 {
		out.Print("No sessions match the given filters\n")
		return nil
	}

	if !stopBatch.force {
		confirmed, err := confirmBatch(cmd, "stop", "stopped", matched)
		if err != nil {
			return err
		}
		if !confirmed {
			return nil
		}
	}

	for _, s := range matched {
		c.SendControl("stop", protocol.StopMsg{SessionID: s.ID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("stopping %s: %s", s.Name, e.Message)
		}
	}

	out.Print("Stopped %d sessions\n", len(matched))
	return nil
}

func init() {
	addBatchFlags(stopCmd, &stopBatch)
	stopCmd.Flags().BoolVar(&stopChildren, "children", false, "also stop all descendant sessions")
	rootCmd.AddCommand(stopCmd)
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: Success

- [ ] **Step 3: Format**

Run: `gofmt -w internal/cli/stop.go`

- [ ] **Step 4: Commit**

```bash
git add internal/cli/stop.go
git commit -m "feat: add --children flag to gr stop

Stops all descendant sessions. With no positional arg, auto-resolves
from GRAITH_SESSION_ID and excludes self. With a positional arg,
includes the named session."
```

---

### Task 7: CLI — `gr delete --children` env auto-resolve

**Files:**
- Modify: `internal/cli/delete.go`

- [ ] **Step 1: Update delete to support env auto-resolve**

In `internal/cli/delete.go`, update the `Args` function to allow no args
when `--children` is set:

Replace lines 25-30:

```go
	Args: func(cmd *cobra.Command, args []string) error {
		if deleteBatch.active() {
			return cobra.NoArgs(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
```

with:

```go
	Args: func(cmd *cobra.Command, args []string) error {
		if deleteChildren && deleteBatch.active() {
			return fmt.Errorf("--children cannot be combined with batch filters")
		}
		if deleteBatch.active() {
			return cobra.NoArgs(cmd, args)
		}
		if deleteChildren {
			return cobra.MaximumNArgs(1)(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
```

Then update the `RunE` to handle the auto-resolve case. Replace
the section inside `RunE` that handles `deleteChildren` (starting after the
batch check at line 36) through the `c.SendControl("delete", ...)` call.

After the batch filter check `if deleteBatch.active() { return deleteBatchRun(cmd) }`,
replace the existing logic (lines 40-82) with:

```go
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		var sessionID string
		var excludeRoot bool

		if deleteChildren && len(args) == 0 {
			sessionID = os.Getenv("GRAITH_SESSION_ID")
			if sessionID == "" {
				return fmt.Errorf("--children with no session arg requires GRAITH_SESSION_ID to be set")
			}
			excludeRoot = true
		} else {
			session, err := resolveSessionInfo(c, args[0])
			if err != nil {
				return err
			}
			sessionID = session.ID

			if !deleteBatch.force && session.WorktreePath != "" && !session.InPlace {
				confirmed, err := confirmDelete(session)
				if err != nil {
					return err
				}
				if !confirmed {
					return nil
				}
			}
		}

		c.SendControl("delete", protocol.DeleteMsg{
			SessionID:   sessionID,
			Children:    deleteChildren,
			ExcludeRoot: excludeRoot,
		})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		if deleteChildren {
			var result struct {
				Deleted []string `json:"deleted"`
			}
			protocol.DecodePayload(resp, &result)
			out.Print("Deleted %d sessions\n", len(result.Deleted))
		} else {
			out.Print("Session deleted\n")
		}
		return nil
```

Add `"os"` to the imports if not already present.

- [ ] **Step 2: Remove the duplicate batch/children check**

The original `RunE` had this check at lines 33-35:

```go
		if deleteChildren && deleteBatch.active() {
			return fmt.Errorf("--children cannot be combined with batch filters")
		}
```

This is now handled in the `Args` function, so remove it from `RunE`.

- [ ] **Step 3: Verify build and format**

Run: `go build ./... && gofmt -w internal/cli/delete.go`
Expected: Success

- [ ] **Step 4: Commit**

```bash
git add internal/cli/delete.go
git commit -m "feat: auto-resolve GRAITH_SESSION_ID for delete --children

When --children is used with no positional arg, resolves the current
session from env and sets ExcludeRoot to avoid deleting the caller."
```

---

### Task 8: CLI — `gr msg send --children` and `--parent`

**Files:**
- Modify: `internal/cli/msg.go`

- [ ] **Step 1: Add flag variables and update init**

In `internal/cli/msg.go`, add to the `msgSend` var block (after `msgSendQuiet`):

```go
	msgSendChildren bool
	msgSendParent   bool
```

In `init()`, after the existing `msgSendCmd` flag registrations (around line 354),
add:

```go
	msgSendCmd.Flags().BoolVar(&msgSendChildren, "children", false, "send to all direct child sessions")
	msgSendCmd.Flags().BoolVar(&msgSendParent, "parent", false, "send to parent session")
	msgSendCmd.MarkFlagsMutuallyExclusive("children", "parent")
```

- [ ] **Step 2: Update args validation on msgSendCmd**

Replace the `Args` and `ValidArgsFunction` fields on `msgSendCmd` (lines 93-94):

```go
	Args:              cobra.RangeArgs(1, 2),
	ValidArgsFunction: completeSessionNames,
```

with:

```go
	Args: func(cmd *cobra.Command, args []string) error {
		if msgSendChildren || msgSendParent {
			return cobra.MaximumNArgs(1)(cmd, args)
		}
		return cobra.RangeArgs(1, 2)(cmd, args)
	},
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if msgSendChildren || msgSendParent {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return completeSessionNames(cmd, args, toComplete)
	},
```

- [ ] **Step 3: Update RunE to dispatch on flags**

In `msgSendCmd.RunE`, add an early dispatch at the top of the function
(before the existing `client.Connect` call):

```go
		if msgSendChildren {
			return msgSendChildrenRun(args)
		}
		if msgSendParent {
			return msgSendParentRun(args)
		}
```

- [ ] **Step 4: Add the helper to resolve the current session's full info**

Add this function to `msg.go`:

```go
func resolveCurrentSessionInfo(c *client.Client) (*protocol.SessionInfo, error) {
	currentID := os.Getenv("GRAITH_SESSION_ID")
	if currentID == "" {
		return nil, fmt.Errorf("GRAITH_SESSION_ID is not set; run this from inside a graith session")
	}

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, err
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, err
	}
	for i, s := range list.Sessions {
		if s.ID == currentID {
			return &list.Sessions[i], nil
		}
	}
	return nil, fmt.Errorf("current session %q not found in daemon", currentID)
}
```

- [ ] **Step 5: Implement msgSendChildrenRun**

Add this function to `msg.go`:

```go
func msgSendChildrenRun(args []string) error {
	body, err := resolveBody(args, msgSendFile)
	if err != nil {
		return err
	}

	senderID, senderName := detectSender()

	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	currentID := os.Getenv("GRAITH_SESSION_ID")
	if currentID == "" {
		return fmt.Errorf("--children requires GRAITH_SESSION_ID to be set")
	}

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return err
	}

	var children []protocol.SessionInfo
	for _, s := range list.Sessions {
		if s.ParentID == currentID {
			children = append(children, s)
		}
	}
	if len(children) == 0 {
		return fmt.Errorf("no child sessions found")
	}

	var sentTo []string
	for _, child := range children {
		c.SendControl("msg_pub", protocol.MsgPubMsg{
			Stream:     "inbox:" + child.ID,
			Body:       body,
			SenderID:   senderID,
			SenderName: senderName,
			ThreadID:   msgSendThreadID,
			ReplyTo:    msgSendReplyTo,
		})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("sending to %s: %s", child.Name, e.Message)
		}
		sentTo = append(sentTo, child.Name)

		if !msgSendQuiet {
			sender := senderName
			if sender == "" {
				sender = senderID
			}
			hint := fmt.Sprintf("New message from %s. Read: gr msg sub --topic inbox:%s --all | Reply: gr msg send --parent \"<reply>\"", sender, child.ID)
			c.SendControl("type", protocol.TypeMsg{
				SessionID: child.ID,
				Input:     hint,
			})
			typeResp, err := c.ReadControlResponse()
			if err != nil || typeResp.Type == "error" {
				// Child may be stopped — skip notification, message is delivered
			}
		}
	}

	if jsonOutput {
		return out.JSON(struct {
			SentTo []string `json:"sent_to"`
			Count  int      `json:"count"`
		}{sentTo, len(sentTo)})
	}
	out.Print("Sent to %d child sessions\n", len(sentTo))
	return nil
}
```

- [ ] **Step 6: Implement msgSendParentRun**

Add this function to `msg.go`:

```go
func msgSendParentRun(args []string) error {
	body, err := resolveBody(args, msgSendFile)
	if err != nil {
		return err
	}

	senderID, senderName := detectSender()

	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	current, err := resolveCurrentSessionInfo(c)
	if err != nil {
		return err
	}
	if current.ParentID == "" {
		return fmt.Errorf("current session has no parent")
	}

	c.SendControl("msg_pub", protocol.MsgPubMsg{
		Stream:     "inbox:" + current.ParentID,
		Body:       body,
		SenderID:   senderID,
		SenderName: senderName,
		ThreadID:   msgSendThreadID,
		ReplyTo:    msgSendReplyTo,
	})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}
	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)
		return fmt.Errorf("%s", e.Message)
	}

	if !msgSendQuiet {
		sender := senderName
		if sender == "" {
			sender = senderID
		}
		hint := fmt.Sprintf("New message from %s. Read: gr msg sub --topic inbox:%s --all | Reply: gr msg send --children \"<reply>\"", sender, current.ParentID)
		c.SendControl("type", protocol.TypeMsg{
			SessionID: current.ParentID,
			Input:     hint,
		})
		typeResp, err := c.ReadControlResponse()
		if err != nil || typeResp.Type == "error" {
			// Parent may be stopped — skip notification, message is delivered
		}
	}

	if jsonOutput {
		return out.JSON(json.RawMessage(resp.Payload))
	}
	out.Print("Sent to parent session\n")
	return nil
}
```

- [ ] **Step 7: Verify build and format**

Run: `go build ./... && gofmt -w internal/cli/msg.go`
Expected: Success

- [ ] **Step 8: Commit**

```bash
git add internal/cli/msg.go
git commit -m "feat: add --children and --parent flags to gr msg send

--children sends to all direct child sessions' inboxes.
--parent sends to the parent session's inbox.
Both auto-resolve from GRAITH_SESSION_ID. Notification failures
for stopped sessions are silently skipped."
```

---

### Task 9: Final Verification

**Files:** None (verification only)

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -race`
Expected: PASS — all tests including race detector

- [ ] **Step 2: Run lint checks**

Run: `gofmt -l ./... && go vet ./...`
Expected: No output (everything clean)

- [ ] **Step 3: Build binary**

Run: `go build -o ./gr ./cmd/graith`
Expected: Success

- [ ] **Step 4: Verify --help output for new flags**

Run: `./gr stop --help`
Expected: Shows `--children` flag in help

Run: `./gr delete --help`
Expected: Shows `--children` flag (existing) in help

Run: `./gr msg send --help`
Expected: Shows `--children` and `--parent` flags

Run: `./gr --help`
Expected: Shows `--agent-mode` flag
