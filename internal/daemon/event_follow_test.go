package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func newEventFollowTestSM(t *testing.T) *SessionManager {
	t.Helper()

	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	logDir := filepath.Join(dir, "logs")

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatalf("create log dir: %v", err)
	}

	messages, err := NewMsgStore(filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}

	t.Cleanup(func() { _ = messages.Close() })

	sm := &SessionManager{
		cfg:                config.Default(),
		paths:              config.Paths{StateFile: filepath.Join(dir, "state.json"), DataDir: dataDir, LogDir: logDir},
		state:              NewState(),
		messages:           messages,
		events:             newEventBroker(8),
		prWatch:            newPRWatchState(0),
		log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		lastInboxNotifyAt:  make(map[string]time.Time),
		silentWarned:       make(map[string]bool),
		subtreeDeleteRoots: make(map[string]struct{}),
	}

	return sm
}

func putEventFollowSession(sm *SessionManager, id, parentID, systemKind string) {
	sm.state.Sessions[id] = &SessionState{
		ID:              id,
		Name:            id,
		ParentID:        parentID,
		SystemKind:      systemKind,
		Status:          StatusRunning,
		StatusChangedAt: time.Now().UTC(),
		CreatedAt:       time.Now().UTC(),
	}
}

func newEventFollowCreateSM(t *testing.T) *SessionManager {
	t.Helper()

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.DefaultAgent = "sleeper"
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}, ResumeArgs: []string{"60"}}

	return newSMWithConfig(t, cfg)
}

func TestCreateWithFollowEventsCreatesRuleAtomically(t *testing.T) {
	sm := newEventFollowCreateSM(t)
	putEventFollowSession(sm, "ben", "", "")
	sm.state.EventFollowRules["b10c1646"] = &EventFollowRuleState{
		SourceSessionID: "b10c1646",
		Events:          []string{eventFollowClassCI},
		CreatedAt:       time.Now().UTC().Add(-time.Hour),
		UpdatedAt:       time.Now().UTC().Add(-time.Hour),
		LastDelivered:   map[string]string{eventFollowClassCI: "pr:4:head:stale:state:failing"},
	}

	created, err := sm.Create(CreateOpts{
		ID:           "b10c1646",
		Name:         "bairn-create",
		AgentName:    "sleeper",
		NoRepo:       true,
		ParentID:     "ben",
		FollowEvents: []string{"ci"},
		Rows:         24,
		Cols:         80,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	rule := sm.state.EventFollowRules[created.ID]
	if rule == nil {
		t.Fatal("follow rule missing after create")
	}

	if strings.Join(rule.Events, ",") != "ci" || rule.SourceSessionID != created.ID {
		t.Fatalf("follow rule = %+v, want ci rule for created child", rule)
	}

	if len(rule.LastDelivered) != 0 {
		t.Fatalf("create reused stale delivery cursor: %+v", rule.LastDelivered)
	}

	if created.ParentID != "ben" {
		t.Fatalf("created parent = %q, want ben", created.ParentID)
	}
}

func TestCreateWithFollowEventsRejectsOrchestratorParentRollback(t *testing.T) {
	sm := newEventFollowCreateSM(t)
	putEventFollowSession(sm, "orch", "", SystemKindOrchestrator)

	_, err := sm.Create(CreateOpts{
		ID:           "cafe1646",
		Name:         "bairn-orch",
		AgentName:    "sleeper",
		NoRepo:       true,
		ParentID:     "orch",
		FollowEvents: []string{"ci"},
		Rows:         24,
		Cols:         80,
	})
	if err == nil || !strings.Contains(err.Error(), "system orchestrator") {
		t.Fatalf("Create error = %v, want orchestrator rejection", err)
	}

	if _, ok := sm.state.Sessions["cafe1646"]; ok {
		t.Fatal("session reservation survived invalid follow-events create")
	}

	if _, ok := sm.state.EventFollowRules["cafe1646"]; ok {
		t.Fatal("follow rule survived invalid follow-events create")
	}
}

func TestEventFollowRegistrationListingAndRemoval(t *testing.T) {
	sm := newEventFollowTestSM(t)
	putEventFollowSession(sm, "ben", "", "")
	putEventFollowSession(sm, "bairn", "ben", "")
	putEventFollowSession(sm, "thrawn", "", "")

	parentAuth := authContext{role: roleSession, authenticated: true, sessionID: "ben"}
	peerAuth := authContext{role: roleSession, authenticated: true, sessionID: "thrawn"}

	info, err := sm.FollowEvents(parentAuth, "bairn", []string{"ci"})
	if err != nil {
		t.Fatalf("FollowEvents: %v", err)
	}

	if info.ChildSessionID != "bairn" || info.ParentSessionID != "ben" || strings.Join(info.Events, ",") != "ci" {
		t.Fatalf("follow info = %+v", info)
	}

	if _, err := sm.FollowEvents(peerAuth, "bairn", []string{"ci"}); err == nil {
		t.Fatal("peer session should not be allowed to manage another parent's follow rule")
	}

	if _, err := sm.FollowEvents(parentAuth, "bairn", []string{"review"}); err == nil {
		t.Fatal("unknown event class should be rejected")
	}

	parentRules, err := sm.EventFollowing(parentAuth)
	if err != nil {
		t.Fatalf("EventFollowing(parent): %v", err)
	}

	if len(parentRules) != 1 || parentRules[0].ChildSessionID != "bairn" {
		t.Fatalf("parent rules = %+v, want bairn", parentRules)
	}

	peerRules, err := sm.EventFollowing(peerAuth)
	if err != nil {
		t.Fatalf("EventFollowing(peer): %v", err)
	}

	if len(peerRules) != 0 {
		t.Fatalf("peer should not see sibling rules, got %+v", peerRules)
	}

	humanRules, err := sm.EventFollowing(authContext{role: roleLocalHuman})
	if err != nil {
		t.Fatalf("EventFollowing(human): %v", err)
	}

	if len(humanRules) != 1 {
		t.Fatalf("human rules = %+v, want 1", humanRules)
	}

	removed, err := sm.UnfollowEvents(parentAuth, "bairn", []string{"ci"})
	if err != nil {
		t.Fatalf("UnfollowEvents: %v", err)
	}

	if len(removed.Events) != 0 {
		t.Fatalf("removed rule events = %+v, want none", removed.Events)
	}

	if _, ok := sm.state.EventFollowRules["bairn"]; ok {
		t.Fatal("follow rule still present after unfollow")
	}
}

func TestEventFollowSaveFailureRollsBackInMemoryRule(t *testing.T) {
	errDreich := errors.New("dreich disk")

	tests := map[string]struct {
		setup  func(*SessionManager)
		action func(*SessionManager) error
		assert func(*testing.T, *SessionManager)
	}{
		"new registration": {
			action: func(sm *SessionManager) error {
				_, err := sm.FollowEvents(authContext{role: roleSession, authenticated: true, sessionID: "ben"}, "bairn", []string{"ci"})
				return err
			},
			assert: func(t *testing.T, sm *SessionManager) {
				t.Helper()

				if _, ok := sm.state.EventFollowRules["bairn"]; ok {
					t.Fatal("failed new registration left in-memory rule")
				}
			},
		},
		"update existing": {
			setup: func(sm *SessionManager) {
				sm.state.EventFollowRules["bairn"] = &EventFollowRuleState{
					SourceSessionID: "bairn",
					Events:          []string{eventFollowClassCI},
					CreatedAt:       time.Now().UTC().Add(-time.Hour),
					UpdatedAt:       time.Now().UTC().Add(-time.Hour),
					LastDelivered: map[string]string{
						eventFollowClassCI: "pr:7:head:sha1:state:failing",
					},
				}
			},
			action: func(sm *SessionManager) error {
				_, err := sm.FollowEvents(authContext{role: roleSession, authenticated: true, sessionID: "ben"}, "bairn", []string{"ci,ci"})
				return err
			},
			assert: func(t *testing.T, sm *SessionManager) {
				t.Helper()

				rule := sm.state.EventFollowRules["bairn"]
				if rule == nil || rule.LastDelivered[eventFollowClassCI] != "pr:7:head:sha1:state:failing" {
					t.Fatalf("failed update did not restore prior rule: %+v", rule)
				}
			},
		},
		"remove existing": {
			setup: func(sm *SessionManager) {
				sm.state.EventFollowRules["bairn"] = &EventFollowRuleState{
					SourceSessionID: "bairn",
					Events:          []string{eventFollowClassCI},
					CreatedAt:       time.Now().UTC(),
					UpdatedAt:       time.Now().UTC(),
					LastDelivered: map[string]string{
						eventFollowClassCI: "pr:8:head:sha2:state:passing",
					},
				}
			},
			action: func(sm *SessionManager) error {
				_, err := sm.UnfollowEvents(authContext{role: roleSession, authenticated: true, sessionID: "ben"}, "bairn", nil)
				return err
			},
			assert: func(t *testing.T, sm *SessionManager) {
				t.Helper()

				rule := sm.state.EventFollowRules["bairn"]
				if rule == nil || strings.Join(rule.Events, ",") != eventFollowClassCI {
					t.Fatalf("failed removal did not restore prior rule: %+v", rule)
				}
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			sm := newEventFollowTestSM(t)
			putEventFollowSession(sm, "ben", "", "")
			putEventFollowSession(sm, "bairn", "ben", "")

			if test.setup != nil {
				test.setup(sm)
			}

			sm.saveStateFault = func() error { return errDreich }

			err := test.action(sm)
			if !errors.Is(err, errDreich) {
				t.Fatalf("action error = %v, want %v", err, errDreich)
			}

			test.assert(t, sm)
		})
	}
}

func TestEventFollowRejectsSystemOrchestratorParent(t *testing.T) {
	sm := newEventFollowTestSM(t)
	putEventFollowSession(sm, "orch", "", SystemKindOrchestrator)
	putEventFollowSession(sm, "bairn", "orch", "")

	if _, err := sm.FollowEvents(authContext{role: roleLocalHuman}, "bairn", []string{"ci"}); err == nil {
		t.Fatal("system orchestrator parent should be rejected")
	}

	sm.state.EventFollowRules["bairn"] = &EventFollowRuleState{
		SourceSessionID: "bairn",
		Events:          []string{eventFollowClassCI},
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		LastDelivered:   make(map[string]string),
	}

	sm.deliverFollowedCIEvent(context.Background(), prWatchTarget{id: "bairn", name: "bairn", branch: "bairn"}, "d0ugal/graith", prData{
		Number: 8, State: "open", HeadRefOid: "sha1", CIState: "failing", FailingChecks: []string{"build"},
	})

	msgs, err := sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("read orchestrator inbox: %v", err)
	}

	if len(msgs) != 0 {
		t.Fatalf("orchestrator received forwarded event: %+v", msgs)
	}

	if _, ok := sm.state.EventFollowRules["bairn"]; ok {
		t.Fatal("stale rule under orchestrator parent should be removed")
	}
}

func TestEventFollowReparentMoveAndDisable(t *testing.T) {
	sm := newEventFollowTestSM(t)
	putEventFollowSession(sm, "ben", "", "")
	putEventFollowSession(sm, "canny", "", "")
	putEventFollowSession(sm, "orch", "", SystemKindOrchestrator)
	putEventFollowSession(sm, "bairn", "ben", "")

	if _, err := sm.FollowEvents(authContext{role: roleSession, authenticated: true, sessionID: "ben"}, "bairn", []string{"ci"}); err != nil {
		t.Fatalf("FollowEvents: %v", err)
	}

	newParent := "canny"
	if _, err := sm.UpdateMetadata("bairn", SessionUpdate{ParentID: &newParent}); err != nil {
		t.Fatalf("UpdateMetadata reparent: %v", err)
	}

	if _, ok := sm.state.EventFollowRules["bairn"]; !ok {
		t.Fatal("rule should move to the new parent on valid reparent")
	}

	oldRules, _ := sm.EventFollowing(authContext{role: roleSession, authenticated: true, sessionID: "ben"})
	if len(oldRules) != 0 {
		t.Fatalf("old parent still sees moved rule: %+v", oldRules)
	}

	newRules, _ := sm.EventFollowing(authContext{role: roleSession, authenticated: true, sessionID: "canny"})
	if len(newRules) != 1 || newRules[0].ParentSessionID != "canny" {
		t.Fatalf("new parent rules = %+v, want moved rule", newRules)
	}

	orchParent := "orch"
	if _, err := sm.UpdateMetadata("bairn", SessionUpdate{ParentID: &orchParent}); err != nil {
		t.Fatalf("UpdateMetadata reparent to orchestrator: %v", err)
	}

	if _, ok := sm.state.EventFollowRules["bairn"]; ok {
		t.Fatal("rule should be disabled when reparented under the system orchestrator")
	}
}

func TestFollowedCIEventDeliveryDedupNoCascadeAndRestart(t *testing.T) {
	sm := newEventFollowTestSM(t)
	putEventFollowSession(sm, "gran", "", "")
	putEventFollowSession(sm, "ben", "gran", "")
	putEventFollowSession(sm, "bairn", "ben", "")
	sm.state.EventFollowRules["bairn"] = &EventFollowRuleState{
		SourceSessionID: "bairn",
		Events:          []string{eventFollowClassCI},
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		LastDelivered:   make(map[string]string),
	}

	sm.state.EventFollowRules["ben"] = &EventFollowRuleState{
		SourceSessionID: "ben",
		Events:          []string{eventFollowClassCI},
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		LastDelivered:   make(map[string]string),
	}
	if err := sm.saveState(); err != nil {
		t.Fatalf("save initial state: %v", err)
	}

	sub, unsub := sm.events.Subscribe()
	defer unsub()

	data := prData{
		Number: 7, State: "open", URL: "https://github.com/d0ugal/graith/pull/7",
		HeadRefOid: "sha1", CIState: "failing", FailingChecks: []string{"build"},
		CIPending: 2, CIPassed: 3, CITotal: 6,
	}
	sm.deliverFollowedCIEvent(context.Background(), prWatchTarget{id: "bairn", name: "bairn", branch: "dreich"}, "d0ugal/graith", data)

	msgs, err := sm.messages.Read("inbox:ben", "", false, "")
	if err != nil {
		t.Fatalf("read parent inbox: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("parent inbox messages = %d, want 1", len(msgs))
	}

	body := msgs[0].Body
	for _, want := range []string{"Forwarded child session event", "bairn", "PR #7", "Head SHA: sha1", "CI is failing", "build", "Pending checks: 2", "daemon-authored"} {
		if !strings.Contains(body, want) {
			t.Fatalf("forwarded body missing %q:\n%s", want, body)
		}
	}

	if !msgs[0].System || msgs[0].SenderID != systemSenderID {
		t.Fatalf("forwarded message sender = %+v, want daemon system sender", msgs[0])
	}

	grandparentMsgs, err := sm.messages.Read("inbox:gran", "", false, "")
	if err != nil {
		t.Fatalf("read grandparent inbox: %v", err)
	}

	if len(grandparentMsgs) != 0 {
		t.Fatalf("grandparent should not receive cascaded child event, got %+v", grandparentMsgs)
	}

	if sm.state.Sessions["ben"].CI.State != "" || sm.state.Sessions["ben"].PullRequest.Number != 0 {
		t.Fatalf("parent PR/CI state was mutated: %+v / %+v", sm.state.Sessions["ben"].PullRequest, sm.state.Sessions["ben"].CI)
	}

	select {
	case event := <-sub:
		if !event.Forwarded || event.EventClass != eventFollowClassCI ||
			event.SourceSessionID != "bairn" || event.DestinationSessionID != "ben" ||
			event.CIState != "failing" || event.PRNumber != 7 || event.HeadRefOID != "sha1" {
			t.Fatalf("event stream payload = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forwarded event stream payload")
	}

	sm.deliverFollowedCIEvent(context.Background(), prWatchTarget{id: "bairn", name: "bairn", branch: "dreich"}, "d0ugal/graith", data)

	msgs, err = sm.messages.Read("inbox:ben", "", false, "")
	if err != nil {
		t.Fatalf("read parent inbox after duplicate: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("duplicate CI observation produced %d messages, want 1", len(msgs))
	}

	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if got := loaded.EventFollowRules["bairn"].LastDelivered[eventFollowClassCI]; got != ciFollowDedupKey(data) {
		t.Fatalf("persisted CI cursor = %q, want %q", got, ciFollowDedupKey(data))
	}

	restarted := newEventFollowTestSM(t)
	restarted.paths.StateFile = sm.paths.StateFile
	restarted.messages = sm.messages
	restarted.state = loaded
	restarted.deliverFollowedCIEvent(context.Background(), prWatchTarget{id: "bairn", name: "bairn", branch: "dreich"}, "d0ugal/graith", data)

	msgs, err = sm.messages.Read("inbox:ben", "", false, "")
	if err != nil {
		t.Fatalf("read parent inbox after restart duplicate: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("restart duplicate produced %d messages, want 1", len(msgs))
	}
}

func TestFollowedCIEventSaveFailureDoesNotAdvanceCursor(t *testing.T) {
	errDreich := errors.New("dreich disk")

	tests := map[string]struct {
		previous string
		want     string
	}{
		"without previous cursor": {},
		"with previous cursor": {
			previous: "pr:7:head:sha1:state:failing",
			want:     "pr:7:head:sha1:state:failing",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			sm := newEventFollowTestSM(t)
			putEventFollowSession(sm, "ben", "", "")
			putEventFollowSession(sm, "bairn", "ben", "")

			rule := &EventFollowRuleState{
				SourceSessionID: "bairn",
				Events:          []string{eventFollowClassCI},
				CreatedAt:       time.Now().UTC(),
				UpdatedAt:       time.Now().UTC(),
				LastDelivered:   make(map[string]string),
			}
			if test.previous != "" {
				rule.LastDelivered[eventFollowClassCI] = test.previous
			}

			sm.state.EventFollowRules["bairn"] = rule

			data := prData{
				Number: 7, State: "open", URL: "https://github.com/d0ugal/graith/pull/7",
				HeadRefOid: "sha2", CIState: "passing", CIPassed: 5, CITotal: 5,
			}
			sm.saveStateFault = func() error { return errDreich }

			sm.deliverFollowedCIEvent(context.Background(), prWatchTarget{id: "bairn", name: "bairn"}, "d0ugal/graith", data)

			if got := sm.state.EventFollowRules["bairn"].LastDelivered[eventFollowClassCI]; got != test.want {
				t.Fatalf("failed save advanced cursor to %q, want %q", got, test.want)
			}

			msgs, err := sm.messages.Read("inbox:ben", "", false, "")
			if err != nil {
				t.Fatalf("read parent inbox: %v", err)
			}

			if len(msgs) != 0 {
				t.Fatalf("failed save delivered %d messages, want none", len(msgs))
			}

			sm.saveStateFault = nil
			sm.deliverFollowedCIEvent(context.Background(), prWatchTarget{id: "bairn", name: "bairn"}, "d0ugal/graith", data)

			if got, want := sm.state.EventFollowRules["bairn"].LastDelivered[eventFollowClassCI], ciFollowDedupKey(data); got != want {
				t.Fatalf("retry cursor = %q, want %q", got, want)
			}

			msgs, err = sm.messages.Read("inbox:ben", "", false, "")
			if err != nil {
				t.Fatalf("read parent inbox after retry: %v", err)
			}

			if len(msgs) != 1 {
				t.Fatalf("retry delivered %d messages, want 1", len(msgs))
			}
		})
	}
}

func TestFollowedCIEventNotifyFailureRestoresPriorCursor(t *testing.T) {
	sm := newEventFollowTestSM(t)
	putEventFollowSession(sm, "ben", "", "")
	putEventFollowSession(sm, "bairn", "ben", "")

	previous := "pr:7:head:sha1:state:failing"
	sm.state.EventFollowRules["bairn"] = &EventFollowRuleState{
		SourceSessionID: "bairn",
		Events:          []string{eventFollowClassCI},
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		LastDelivered:   map[string]string{eventFollowClassCI: previous},
	}
	sm.messages = nil

	sm.deliverFollowedCIEvent(context.Background(), prWatchTarget{id: "bairn", name: "bairn"}, "d0ugal/graith", prData{
		Number: 7, State: "open", URL: "https://github.com/d0ugal/graith/pull/7",
		HeadRefOid: "sha2", CIState: "passing", CIPassed: 5, CITotal: 5,
	})

	if got := sm.state.EventFollowRules["bairn"].LastDelivered[eventFollowClassCI]; got != previous {
		t.Fatalf("notify failure cursor = %q, want previous %q", got, previous)
	}
}

func TestEventFollowHardDeleteRemovesSourceRule(t *testing.T) {
	sm := newEventFollowTestSM(t)
	putEventFollowSession(sm, "ben", "", "")
	putEventFollowSession(sm, "bairn", "ben", "")
	sm.state.Sessions["bairn"].Status = StatusStopped
	sm.state.EventFollowRules["bairn"] = &EventFollowRuleState{
		SourceSessionID: "bairn",
		Events:          []string{eventFollowClassCI},
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		LastDelivered:   make(map[string]string),
	}

	if err := sm.saveState(); err != nil {
		t.Fatalf("save initial state: %v", err)
	}

	if err := sm.Delete("bairn"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if _, ok := loaded.EventFollowRules["bairn"]; ok {
		t.Fatal("source rule survived hard delete")
	}
}
