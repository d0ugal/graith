package cli

import (
	"errors"
	"io"
	"testing"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
)

// setDiscardOutForDelete swaps the package out writer for one that discards, in
// the requested JSON mode, restoring the original on cleanup.
func setDiscardOutForDelete(t *testing.T, jsonMode bool) {
	t.Helper()

	orig := out

	t.Cleanup(func() { out = orig })

	out = output.NewWithWriter(jsonMode, io.Discard)
}

// TestDeleteCmdArgsValidation exercises the custom Args validator on deleteCmd,
// which gates the mutually-exclusive --children / batch-filter combinations and
// the differing arity each mode allows.
func TestDeleteCmdArgsValidation(t *testing.T) {
	origChildren := deleteChildren
	origBatch := deleteBatch

	t.Cleanup(func() {
		deleteChildren = origChildren
		deleteBatch = origBatch
	})

	tests := []struct {
		name     string
		children bool
		batch    batchFlags
		args     []string
		wantErr  bool
	}{
		{name: "children with batch filter rejected", children: true, batch: batchFlags{stopped: true}, args: nil, wantErr: true},
		{name: "batch filter takes no args", batch: batchFlags{repo: "croft"}, args: nil, wantErr: false},
		{name: "batch filter rejects positional arg", batch: batchFlags{repo: "croft"}, args: []string{"braw"}, wantErr: true},
		{name: "children allows zero args", children: true, args: nil, wantErr: false},
		{name: "children allows one arg", children: true, args: []string{"ben"}, wantErr: false},
		{name: "children rejects two args", children: true, args: []string{"ben", "brae"}, wantErr: true},
		{name: "plain requires exactly one arg", args: []string{"braw"}, wantErr: false},
		{name: "plain rejects zero args", args: nil, wantErr: true},
		{name: "plain rejects two args", args: []string{"braw", "canny"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deleteChildren = tt.children
			deleteBatch = tt.batch

			err := deleteCmd.Args(deleteCmd, tt.args)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestConfirmDeleteAutoConfirmsWhenClean verifies a session with no dirty files
// and no unpushed commits is deleted without prompting.
func TestConfirmDeleteAutoConfirmsWhenClean(t *testing.T) {
	setDiscardOutForDelete(t, false)
	stubGitChecks(t, nil, nil, nil, nil)

	session := &protocol.SessionInfo{
		Name:         "braw",
		RepoName:     "croft",
		WorktreePath: "/bothy/braw",
		BaseBranch:   "main",
	}

	confirmed, err := confirmDelete(session)
	if err != nil {
		t.Fatalf("confirmDelete error: %v", err)
	}

	if !confirmed {
		t.Fatalf("expected auto-confirm on clean session, got false")
	}
}

// TestConfirmDeleteJSONModeRefusesDirty verifies that in JSON mode a session
// with unsaved work cannot be interactively confirmed and errors instead.
func TestConfirmDeleteJSONModeRefusesDirty(t *testing.T) {
	setDiscardOutForDelete(t, true)
	stubGitChecks(t,
		map[string][]string{"/bothy/dreich": {" M loch.go"}},
		nil, nil, nil,
	)

	session := &protocol.SessionInfo{
		Name:         "dreich",
		RepoName:     "croft",
		WorktreePath: "/bothy/dreich",
		BaseBranch:   "main",
	}

	confirmed, err := confirmDelete(session)
	if err == nil {
		t.Fatalf("expected error in JSON mode with dirty session")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}

// TestConfirmDeleteNonTerminalRefusesUnpushed covers the non-terminal branch:
// with unpushed commits and no TTY (the test environment), confirmDelete must
// refuse rather than prompt.
func TestConfirmDeleteNonTerminalRefusesUnpushed(t *testing.T) {
	setDiscardOutForDelete(t, false)
	stubGitChecks(t, nil, nil,
		map[string][]string{"/bothy/thrawn": {"abc auld"}},
		nil,
	)

	session := &protocol.SessionInfo{
		Name:         "thrawn",
		RepoName:     "croft",
		WorktreePath: "/bothy/thrawn",
		BaseBranch:   "main",
	}

	confirmed, err := confirmDelete(session)
	if err == nil {
		t.Fatalf("expected error with no TTY and unpushed commits")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}

// TestConfirmDeleteAggregatesIncludesGitFailure verifies that a failed git
// check on an included repo marks the session as having work (gitFailed) and so
// refuses non-interactive deletion.
func TestConfirmDeleteAggregatesIncludesGitFailure(t *testing.T) {
	setDiscardOutForDelete(t, true)
	stubGitChecks(t, nil,
		map[string]error{"/bothy/skelf": errors.New("scunner")},
		nil, nil,
	)

	session := &protocol.SessionInfo{
		Name:         "clachan",
		RepoName:     "croft",
		WorktreePath: "/bothy/clachan",
		BaseBranch:   "main",
		Includes: []protocol.IncludedRepoInfo{
			{RepoName: "neep", WorktreePath: "/bothy/skelf", BaseBranch: "main"},
		},
	}

	confirmed, err := confirmDelete(session)
	if err == nil {
		t.Fatalf("expected error when an included repo git check fails")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}
