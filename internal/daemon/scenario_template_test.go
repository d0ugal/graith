package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func testScenarioRenderState() *ScenarioRenderState {
	return &ScenarioRenderState{
		AuthoredName: "parallel-{caller}-{date}-{short_id}",
		ScenarioID:   "sc-abcdef12",
		ShortID:      "abcdef12",
		RenderedAt:   time.Date(2026, 7, 18, 1, 2, 3, 456, time.UTC),
		Caller:       ScenarioRenderIdentityState{SessionID: "caller-id", Name: "canny"},
		Parent:       ScenarioRenderIdentityState{SessionID: "parent-id", Name: "orchestrator"},
		Initiator:    ScenarioRenderIdentityState{SessionID: "initiator-id", Name: "braw"},
	}
}

func TestRenderScenarioStartRendersNamesAndReferencesOnce(t *testing.T) {
	state := testScenarioRenderState()
	msg := protocol.ScenarioStartMsg{
		Name: "parallel-{caller}-{parent}-{initiator}-{date}-{time}-{datetime}-{scenario_id}-{short_id}",
		Sessions: []protocol.ScenarioSessionInput{
			{Name: "{initiator}", Shared: true},
			{Name: "{scenario}-reviewer", Mirror: "{initiator}"},
			{Name: "{scenario}-writer", Repo: "/croft", Task: "write", DependsOn: []string{"{scenario}-reviewer"}},
		},
		Triggers: []config.TriggerConfig{
			{
				Name:       "complete-review",
				Completion: &config.CompletionConfig{Session: "{scenario}-reviewer"},
				Action: config.ActionConfig{Deliver: config.DeliverConfig{
					Inbox: "{scenario}-reviewer",
				}},
			},
			{
				Name: "watch-review",
				Action: config.ActionConfig{Deliver: config.DeliverConfig{
					Inbox: "{scenario}-{session_name}",
				}},
			},
		},
	}

	got, err := renderScenarioStart(msg, state)
	if err != nil {
		t.Fatal(err)
	}

	wantScenario := "parallel-canny-orchestrator-braw-20260718-010203-20260718t010203z-sc-abcdef12-abcdef12"
	if got.Name != wantScenario {
		t.Fatalf("scenario name = %q, want %q", got.Name, wantScenario)
	}

	if got.Sessions[0].Name != "braw" {
		t.Errorf("shared initiator name = %q, want braw", got.Sessions[0].Name)
	}

	reviewer := wantScenario + "-reviewer"
	writer := wantScenario + "-writer"

	if got.Sessions[1].Name != reviewer || got.Sessions[1].Mirror != "braw" {
		t.Errorf("reviewer = %+v, want name %q mirror braw", got.Sessions[1], reviewer)
	}

	if got.Sessions[2].Name != writer || len(got.Sessions[2].DependsOn) != 1 || got.Sessions[2].DependsOn[0] != reviewer {
		t.Errorf("writer = %+v, want name %q dependency %q", got.Sessions[2], writer, reviewer)
	}

	if got.Triggers[0].Completion.Session != reviewer || got.Triggers[0].Action.Deliver.Inbox != reviewer {
		t.Errorf("completion trigger references were not rendered: %+v", got.Triggers[0])
	}

	if got.Triggers[1].Action.Deliver.Inbox != wantScenario+"-{session_name}" {
		t.Errorf("fire-time trigger variable should remain deferred, got %q", got.Triggers[1].Action.Deliver.Inbox)
	}

	// Rendering a copy must not mutate the authored graph retained for
	// diagnostics or retryable request handling.
	if msg.Sessions[1].Name != "{scenario}-reviewer" || msg.Triggers[0].Completion.Session != "{scenario}-reviewer" {
		t.Fatalf("authored message mutated: %+v", msg)
	}

	if len(state.Members) != 3 || state.Members[1].AuthoredName != "{scenario}-reviewer" || state.Members[1].RenderedName != reviewer {
		t.Fatalf("render member metadata = %+v", state.Members)
	}

	if len(state.References) != 5 {
		t.Fatalf("render references = %+v, want 5", state.References)
	}
}

func TestRenderScenarioStartRejectsUnknownUnavailableAndMalformedTemplates(t *testing.T) {
	tests := []struct {
		name string
		msg  protocol.ScenarioStartMsg
		want string
	}{
		{
			name: "unknown scenario token",
			msg:  protocol.ScenarioStartMsg{Name: "braw-{dreich}", Sessions: []protocol.ScenarioSessionInput{{Name: "canny"}}},
			want: `unknown scenario name template variable "dreich" in scenario.name`,
		},
		{
			name: "scenario self reference",
			msg:  protocol.ScenarioStartMsg{Name: "braw-{scenario}", Sessions: []protocol.ScenarioSessionInput{{Name: "canny"}}},
			want: `template variable "scenario" is not available`,
		},
		{
			name: "unknown member reference token",
			msg: protocol.ScenarioStartMsg{Name: "braw", Sessions: []protocol.ScenarioSessionInput{
				{Name: "canny", Mirror: "{dreich}"},
			}},
			want: `unknown scenario name template variable "dreich" in sessions[0].mirror`,
		},
		{
			name: "unknown trigger token",
			msg: protocol.ScenarioStartMsg{
				Name:     "braw",
				Sessions: []protocol.ScenarioSessionInput{{Name: "canny"}},
				Triggers: []config.TriggerConfig{{Action: config.ActionConfig{Deliver: config.DeliverConfig{Inbox: "{dreich}"}}}},
			},
			want: `unknown scenario name template variable "dreich"`,
		},
		{
			name: "malformed braces",
			msg:  protocol.ScenarioStartMsg{Name: "braw-{caller", Sessions: []protocol.ScenarioSessionInput{{Name: "canny"}}},
			want: "invalid scenario name template syntax",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := renderScenarioStart(test.msg, testScenarioRenderState())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestReserveScenarioIDRetriesPersistedAndConcurrentCollisions(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.state.Scenarios["sc-aaaaaaaa"] = &ScenarioState{ID: "sc-aaaaaaaa", Name: "auld"}

	candidates := []string{"aaaaaaaa", "bbbbbbbb", "bbbbbbbb", "cccccccc"}
	sm.scenarioIDGenerator = func() string {
		candidate := candidates[0]
		candidates = candidates[1:]

		return candidate
	}

	first, releaseFirst, err := sm.reserveScenarioID()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()

	second, releaseSecond, err := sm.reserveScenarioID()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()

	if first != "sc-bbbbbbbb" || second != "sc-cccccccc" {
		t.Fatalf("allocated ids = %q, %q", first, second)
	}
}

func TestReserveScenarioIDCollisionRetryIsBounded(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.state.Scenarios["sc-aaaaaaaa"] = &ScenarioState{ID: "sc-aaaaaaaa", Name: "auld"}
	sm.scenarioIDGenerator = func() string { return "aaaaaaaa" }

	_, _, err := sm.reserveScenarioID()
	if err == nil || !strings.Contains(err.Error(), "exhausted 32 attempts") {
		t.Fatalf("error = %v, want bounded allocation failure", err)
	}
}

func TestScenarioRenderInfoPreservesNanosecondSnapshot(t *testing.T) {
	info := scenarioRenderInfo(testScenarioRenderState())
	if info.RenderedAt != "2026-07-18T01:02:03.000000456Z" {
		t.Fatalf("rendered_at = %q", info.RenderedAt)
	}

	if info.Caller.Name != "canny" || info.Parent.Name != "orchestrator" || info.Initiator.Name != "braw" {
		t.Fatalf("identities = %+v %+v %+v", info.Caller, info.Parent, info.Initiator)
	}
}
