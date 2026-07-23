package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

type fakeSessionListUseCase struct {
	sessions []protocol.SessionInfo
	deleted  bool
}

func (fake *fakeSessionListUseCase) ListSessions(deleted bool) ([]protocol.SessionInfo, error) {
	fake.deleted = deleted
	return fake.sessions, nil
}

func TestCommandDependenciesInjectSessionListUseCase(t *testing.T) {
	fake := &fakeSessionListUseCase{sessions: []protocol.SessionInfo{{ID: "braw"}}}
	deps := commandDependencies{
		cfg:         config.Default(),
		out:         output.New(false),
		listSession: fake,
	}
	ctx := withCommandDependencies(context.Background(), deps)

	got, err := commandDeps(ctx).listSession.ListSessions(true)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if !fake.deleted {
		t.Fatal("fake did not receive deleted filter")
	}

	if len(got) != 1 || got[0].ID != "braw" {
		t.Fatalf("sessions = %#v, want braw", got)
	}
}

func TestRunListUsesInjectedSessionListUseCase(t *testing.T) {
	fake := &fakeSessionListUseCase{sessions: []protocol.SessionInfo{{ID: "braw", Name: "braw"}}}

	var buf bytes.Buffer

	deps := commandDependencies{
		cfg:         config.Default(),
		out:         output.NewWithWriter(false, &buf),
		listSession: fake,
	}

	oldQuiet, oldJSON, oldLabels := listQuiet, jsonOutput, listLabels

	t.Cleanup(func() { listQuiet, jsonOutput, listLabels = oldQuiet, oldJSON, oldLabels })

	listQuiet, jsonOutput, listLabels = true, false, nil

	cmd := &cobra.Command{}

	cmd.SetContext(withCommandDependencies(context.Background(), deps))
	cmd.SetOut(&buf)

	if err := runList(cmd, nil); err != nil {
		t.Fatalf("runList: %v", err)
	}

	if !bytes.Contains(buf.Bytes(), []byte("braw\n")) {
		t.Fatalf("output = %q, want injected session", buf.String())
	}
}
