package daemon

import (
	"sync"
	"testing"
	"time"
)

func newTestSMWithSessions(sessions map[string]*SessionState) *SessionManager {
	tokenIdx := make(map[string]string, len(sessions))
	for _, s := range sessions {
		if s.Token != "" {
			tokenIdx[s.Token] = s.ID
		}
	}

	return &SessionManager{
		state:      &State{Sessions: sessions},
		tokenIndex: tokenIdx,
		mu:         sync.RWMutex{},
	}
}

func TestResolveAuth_LocalTokens(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw": {ID: "braw", Token: "tok-braw"},
	})
	sm.humanToken = "human-canny"

	tests := []struct {
		name      string
		token     string
		wantRole  authRole
		wantID    string
		wantError bool
	}{
		{name: "canny human token", token: "human-canny", wantRole: roleLocalHuman},
		{name: "braw session token", token: "tok-braw", wantRole: roleSession, wantID: "braw"},
		{name: "thrawn empty token", wantRole: roleNone, wantError: true},
		{name: "dreich unknown token", token: "tok-dreich", wantRole: roleNone, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, err := resolveAuth(sm, tt.token, ConnOrigin{}, "")
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v, wantError %v", err, tt.wantError)
			}
			if auth.role != tt.wantRole {
				t.Errorf("role = %d, want %d", auth.role, tt.wantRole)
			}
			if auth.sessionID != tt.wantID {
				t.Errorf("session = %q, want %q", auth.sessionID, tt.wantID)
			}
		})
	}
}

func TestResolveAuth_InvalidToken(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw": {ID: "braw", Token: "tok-braw"},
	})

	_, err := resolveAuth(sm, "tok-thrawn", ConnOrigin{}, "")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestCheckTarget_AlwaysAllowed(t *testing.T) {
	sm := newTestSMWithSessions(nil)

	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkTarget(sm, "any", authAlwaysAllowed); err != nil {
		t.Errorf("expected allowed, got: %v", err)
	}
}

func TestCheckTarget_HumanOnly_RejectsAgent(t *testing.T) {
	sm := newTestSMWithSessions(nil)

	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkTarget(sm, "", authHumanOnly); err == nil {
		t.Error("expected rejection for authenticated session")
	}
}

func TestCheckTarget_HumanOnly_AllowsHuman(t *testing.T) {
	sm := newTestSMWithSessions(nil)

	auth := authContext{}
	if err := auth.checkTarget(sm, "", authHumanOnly); err != nil {
		t.Errorf("expected allowed for human, got: %v", err)
	}
}

func TestCheckTarget_SelfOnly_AllowsSelf(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw": {ID: "braw"},
	})

	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkTarget(sm, "braw", authSelfOnly); err != nil {
		t.Errorf("expected allowed for self, got: %v", err)
	}
}

func TestCheckTarget_SelfOnly_RejectsOther(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw":  {ID: "braw"},
		"canny": {ID: "canny"},
	})

	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkTarget(sm, "canny", authSelfOnly); err == nil {
		t.Error("expected rejection for other session")
	}
}

func TestCheckTarget_SelfOrDescendant_AllowsSelf(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw": {ID: "braw"},
	})

	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkTarget(sm, "braw", authSelfOrDescendant); err != nil {
		t.Errorf("expected allowed for self, got: %v", err)
	}
}

func TestCheckTarget_SelfOrDescendant_AllowsChild(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben":   {ID: "ben"},
		"bairn": {ID: "bairn", ParentID: "ben"},
	})

	auth := authContext{sessionID: "ben", authenticated: true}
	if err := auth.checkTarget(sm, "bairn", authSelfOrDescendant); err != nil {
		t.Errorf("expected allowed for child, got: %v", err)
	}
}

func TestCheckTarget_SelfOrDescendant_AllowsGrandchild(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"brae":      {ID: "brae"},
		"bairn":     {ID: "bairn", ParentID: "brae"},
		"wee-bairn": {ID: "wee-bairn", ParentID: "bairn"},
	})

	auth := authContext{sessionID: "brae", authenticated: true}
	if err := auth.checkTarget(sm, "wee-bairn", authSelfOrDescendant); err != nil {
		t.Errorf("expected allowed for grandchild, got: %v", err)
	}
}

func TestCheckTarget_SelfOrDescendant_RejectsSibling(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben":    {ID: "ben"},
		"bairn1": {ID: "bairn1", ParentID: "ben"},
		"bairn2": {ID: "bairn2", ParentID: "ben"},
	})

	auth := authContext{sessionID: "bairn1", authenticated: true}
	if err := auth.checkTarget(sm, "bairn2", authSelfOrDescendant); err == nil {
		t.Error("expected rejection for sibling")
	}
}

func TestCheckTarget_SelfOrDescendant_RejectsParent(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben":   {ID: "ben"},
		"bairn": {ID: "bairn", ParentID: "ben"},
	})

	auth := authContext{sessionID: "bairn", authenticated: true}
	if err := auth.checkTarget(sm, "ben", authSelfOrDescendant); err == nil {
		t.Error("expected rejection for parent (not a descendant)")
	}
}

func TestCheckTarget_SelfOrDescendant_AllowsOrchestrator(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"orch":      {ID: "orch", SystemKind: SystemKindOrchestrator},
		"thrawn":    {ID: "thrawn"},
		"unrelated": {ID: "unrelated"},
	})

	auth := authContext{sessionID: "orch", authenticated: true}
	if err := auth.checkTarget(sm, "thrawn", authSelfOrDescendant); err != nil {
		t.Errorf("expected orchestrator allowed to target any session, got: %v", err)
	}

	if err := auth.checkTarget(sm, "unrelated", authSelfOrDescendant); err != nil {
		t.Errorf("expected orchestrator allowed to target unrelated session, got: %v", err)
	}
}

func TestCheckTarget_SelfOnly_AllowsOrchestrator(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"orch":   {ID: "orch", SystemKind: SystemKindOrchestrator},
		"thrawn": {ID: "thrawn"},
	})

	auth := authContext{sessionID: "orch", authenticated: true}
	if err := auth.checkTarget(sm, "thrawn", authSelfOnly); err != nil {
		t.Errorf("expected orchestrator allowed to target any session, got: %v", err)
	}
}

func TestCheckTarget_SelfOrDescendant_NonOrchestratorStillRejected(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"orch":   {ID: "orch", SystemKind: SystemKindOrchestrator},
		"canny":  {ID: "canny"},
		"thrawn": {ID: "thrawn"},
	})
	// A regular session must not gain elevated access just because an
	// orchestrator exists in the fleet.
	auth := authContext{sessionID: "canny", authenticated: true}
	if err := auth.checkTarget(sm, "thrawn", authSelfOrDescendant); err == nil {
		t.Error("expected non-orchestrator session to be rejected for unrelated target")
	}
}

func TestCheckTarget_Unauthenticated_AllowsEmpty(t *testing.T) {
	sm := newTestSMWithSessions(nil)

	auth := authContext{}
	if err := auth.checkTarget(sm, "", authSelfOnly); err != nil {
		t.Errorf("expected allowed for unauthenticated with no target, got: %v", err)
	}
}

func TestCheckTarget_Unauthenticated_AllowsWithTarget(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw": {ID: "braw"},
	})

	auth := authContext{}
	if err := auth.checkTarget(sm, "braw", authSelfOnly); err != nil {
		t.Errorf("expected allowed for unauthenticated (human) with target, got: %v", err)
	}

	if err := auth.checkTarget(sm, "braw", authSelfOrDescendant); err != nil {
		t.Errorf("expected allowed for unauthenticated (human) with target (descendant rule), got: %v", err)
	}
}

func TestIsDescendantOf(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"brae":      {ID: "brae"},
		"bairn":     {ID: "bairn", ParentID: "brae"},
		"wee-bairn": {ID: "wee-bairn", ParentID: "bairn"},
		"thrawn":    {ID: "thrawn"},
	})

	tests := []struct {
		target, root string
		want         bool
	}{
		{"brae", "brae", true},
		{"bairn", "brae", true},
		{"wee-bairn", "brae", true},
		{"thrawn", "brae", false},
		{"brae", "bairn", false},
		{"thrawn", "bairn", false},
	}
	for _, tt := range tests {
		got := sm.isDescendantOf(tt.target, tt.root)
		if got != tt.want {
			t.Errorf("isDescendantOf(%q, %q) = %v, want %v", tt.target, tt.root, got, tt.want)
		}
	}
}

func TestGenerateToken(t *testing.T) {
	tok, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}

	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64", len(tok))
	}

	tok2, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}

	if tok == tok2 {
		t.Error("two generated tokens should not be equal")
	}
}

func TestMigrateV9ToV10(t *testing.T) {
	state := &State{
		Version: 9,
		Sessions: map[string]*SessionState{
			"braw":  {ID: "braw"},
			"canny": {ID: "canny", Token: "tok-auld"},
		},
	}
	if err := migrateV9ToV10(state); err != nil {
		t.Fatal(err)
	}

	if state.Sessions["braw"].Token == "" {
		t.Error("braw should have a token after migration")
	}

	if state.Sessions["canny"].Token != "tok-auld" {
		t.Error("canny should keep its existing token")
	}
}

func TestRebuildTokenIndex(t *testing.T) {
	sm := &SessionManager{
		state: &State{
			Sessions: map[string]*SessionState{
				"braw":  {ID: "braw", Token: "tok-neep1"},
				"canny": {ID: "canny", Token: "tok-neep2"},
				"kirk":  {ID: "kirk"},
			},
		},
		tokenIndex: make(map[string]string),
	}
	sm.rebuildTokenIndex()

	if sm.tokenIndex["tok-neep1"] != "braw" {
		t.Errorf("tok-neep1 → %q, want braw", sm.tokenIndex["tok-neep1"])
	}

	if sm.tokenIndex["tok-neep2"] != "canny" {
		t.Errorf("tok-neep2 → %q, want canny", sm.tokenIndex["tok-neep2"])
	}

	if len(sm.tokenIndex) != 2 {
		t.Errorf("tokenIndex has %d entries, want 2", len(sm.tokenIndex))
	}
}

func TestParseInboxStream(t *testing.T) {
	tests := []struct {
		stream    string
		wantID    string
		wantInbox bool
	}{
		{"inbox:braw", "braw", true},
		{"inbox:", "", true},
		{"blether-review", "", false},
		{"inboxes", "", false},
	}
	for _, tt := range tests {
		id, isInbox := parseInboxStream(tt.stream)
		if id != tt.wantID || isInbox != tt.wantInbox {
			t.Errorf("parseInboxStream(%q) = (%q, %v), want (%q, %v)", tt.stream, id, isInbox, tt.wantID, tt.wantInbox)
		}
	}
}

func TestResolveAuth_LocalHumanRole(t *testing.T) {
	sm := newTestSMWithSessions(nil)
	sm.humanToken = "human-canny"

	auth, err := resolveAuth(sm, "human-canny", ConnOrigin{Remote: false}, "")
	if err != nil {
		t.Fatal(err)
	}

	if auth.role != roleLocalHuman {
		t.Errorf("role = %d, want roleLocalHuman", auth.role)
	}

	if !auth.isLocalHuman() || !auth.isHuman() {
		t.Error("local human should satisfy isLocalHuman() and isHuman()")
	}
}

func TestResolveAuth_RemoteNoPoPIsRoleNone(t *testing.T) {
	sm := newTestSMWithSessions(nil)

	auth, err := resolveAuth(sm, "", ConnOrigin{Remote: true, Identity: &TailnetIdentity{User: "speir@example.com", Node: "ben"}}, "")
	if err != nil {
		t.Fatal(err)
	}

	if auth.role != roleNone {
		t.Errorf("role = %d, want roleNone for a remote conn without proof-of-possession", auth.role)
	}

	if auth.isHuman() {
		t.Error("roleNone must not be a human")
	}
}

func TestResolveAuth_RemoteHuman(t *testing.T) {
	sm := newPairingSM(t)
	id := TailnetIdentity{User: "speir@example.com", Node: "ben"}
	now := time.Now()

	rid, _, err := sm.AddPendingPairing("bairn", testPubKey(t), id, now)
	if err != nil {
		t.Fatal(err)
	}

	deviceID, token, err := sm.ApprovePairing(rid, false, now)
	if err != nil {
		t.Fatal(err)
	}

	origin := ConnOrigin{Remote: true, Identity: &TailnetIdentity{User: "speir@example.com", Node: "ben"}}

	auth, err := resolveAuth(sm, token, origin, deviceID)
	if err != nil {
		t.Fatal(err)
	}

	if auth.role != roleRemoteHuman {
		t.Errorf("role = %d, want roleRemoteHuman", auth.role)
	}

	if !auth.isHuman() || auth.isLocalHuman() {
		t.Error("remote human: isHuman() true, isLocalHuman() false")
	}

	if auth.deviceID != deviceID {
		t.Errorf("deviceID = %q, want %q", auth.deviceID, deviceID)
	}
}

func TestResolveAuth_RemoteGuestReadOnly(t *testing.T) {
	sm := newPairingSM(t)
	id := TailnetIdentity{User: "speir@example.com", Node: "ben"}
	now := time.Now()
	rid, _, _ := sm.AddPendingPairing("bairn", testPubKey(t), id, now)
	deviceID, token, _ := sm.ApprovePairing(rid, true, now) // readOnly

	origin := ConnOrigin{Remote: true, Identity: &TailnetIdentity{User: "speir@example.com", Node: "ben"}}

	auth, err := resolveAuth(sm, token, origin, deviceID)
	if err != nil {
		t.Fatal(err)
	}

	if auth.role != roleRemoteGuest {
		t.Errorf("role = %d, want roleRemoteGuest", auth.role)
	}

	if auth.isHuman() {
		t.Error("roleRemoteGuest is read-only, not a full human")
	}
}

func TestResolveAuth_RemoteIdentityMismatchIsRoleNone(t *testing.T) {
	sm := newPairingSM(t)
	id := TailnetIdentity{User: "speir@example.com", Node: "ben"}
	now := time.Now()
	rid, _, _ := sm.AddPendingPairing("bairn", testPubKey(t), id, now)
	deviceID, token, _ := sm.ApprovePairing(rid, false, now)

	// Same token + PoP, but the connection now comes from a different node.
	origin := ConnOrigin{Remote: true, Identity: &TailnetIdentity{User: "speir@example.com", Node: "brae"}}

	auth, err := resolveAuth(sm, token, origin, deviceID)
	if err != nil {
		t.Fatal(err)
	}

	if auth.role != roleNone {
		t.Errorf("role = %d, want roleNone when WhoIs identity does not match the paired record", auth.role)
	}
}

func TestResolveAuth_RemoteWrongPoPDeviceIsRoleNone(t *testing.T) {
	sm := newPairingSM(t)
	id := TailnetIdentity{User: "speir@example.com", Node: "ben"}
	now := time.Now()
	rid, _, _ := sm.AddPendingPairing("bairn", testPubKey(t), id, now)
	_, token, _ := sm.ApprovePairing(rid, false, now)

	origin := ConnOrigin{Remote: true, Identity: &TailnetIdentity{User: "speir@example.com", Node: "ben"}}

	// A valid token but PoP proved a *different* device ID → roleNone.
	auth, err := resolveAuth(sm, token, origin, "some-other-device")
	if err != nil {
		t.Fatal(err)
	}

	if auth.role != roleNone {
		t.Errorf("role = %d, want roleNone when PoP device != token device", auth.role)
	}
}

func TestAuthorizeScenarioOp(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben":    {ID: "ben", SystemKind: SystemKindOrchestrator},
		"thrawn": {ID: "thrawn"},
	})
	sm.state.Scenarios = map[string]*ScenarioState{
		"strath": {Name: "strath", OrchestratorID: "ben"},
	}

	// Authorized: orchestrator, no error control message emitted.
	var sentType string

	send := func(typ string, _ any) { sentType = typ }

	if ok := (authContext{sessionID: "ben", authenticated: true, role: roleOrchestrator}).authorizeScenarioOp(sm, "strath", send); !ok || sentType != "" {
		t.Errorf("orchestrator: ok=%v sent=%q, want ok=true, no message", ok, sentType)
	}

	// Denied: unrelated session, an "error" control message is emitted.
	sentType = ""
	if ok := (authContext{sessionID: "thrawn", authenticated: true, role: roleSession}).authorizeScenarioOp(sm, "strath", send); ok || sentType != "error" {
		t.Errorf("unrelated session: ok=%v sent=%q, want ok=false, error message", ok, sentType)
	}
}

func TestAuthorizeTriggerOp(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben":   {ID: "ben", SystemKind: SystemKindOrchestrator},
		"other": {ID: "other"},
	})

	var sentType string

	send := func(typ string, _ any) { sentType = typ }

	if ok := (authContext{sessionID: "ben", authenticated: true, role: roleOrchestrator}).authorizeTriggerOp(sm, send); !ok || sentType != "" {
		t.Errorf("orchestrator: ok=%v sent=%q, want ok=true, no message", ok, sentType)
	}

	sentType = ""
	if ok := (authContext{sessionID: "other", authenticated: true, role: roleSession}).authorizeTriggerOp(sm, send); ok || sentType != "error" {
		t.Errorf("unrelated session: ok=%v sent=%q, want ok=false, error message", ok, sentType)
	}
}

func TestCheckTarget_UnknownRule(t *testing.T) {
	sm := newTestSMWithSessions(nil)
	// A rule value outside the known set must fail closed, not silently allow.
	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkTarget(sm, "braw", authRule(99)); err == nil {
		t.Error("expected an unknown auth rule to be rejected")
	}
}

func TestCheckScenarioOp(t *testing.T) {
	// A three-generation tree under the scenario's orchestrator, plus an
	// unrelated session that must never be able to manage the scenario.
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben":       {ID: "ben", SystemKind: SystemKindOrchestrator},
		"bairn":     {ID: "bairn", ParentID: "ben"},
		"wee-bairn": {ID: "wee-bairn", ParentID: "bairn"},
		"thrawn":    {ID: "thrawn"},
		// A SECOND, unrelated orchestrator: orchestrator elevation must not be
		// global across trees — it may only manage its own scenario.
		"ben-two": {ID: "ben-two", SystemKind: SystemKindOrchestrator},
	})
	sm.state.Scenarios = map[string]*ScenarioState{
		"strath": {Name: "strath", OrchestratorID: "ben"},
	}

	tests := []struct {
		name     string
		auth     authContext
		scenario string
		wantErr  bool
	}{
		{"local human may manage any scenario", authContext{role: roleLocalHuman}, "strath", false},
		{"remote human may manage any scenario", authContext{role: roleRemoteHuman}, "strath", false},
		{"scenario orchestrator allowed", authContext{sessionID: "ben", authenticated: true, role: roleOrchestrator}, "strath", false},
		{"descendant of orchestrator allowed", authContext{sessionID: "wee-bairn", authenticated: true, role: roleSession}, "strath", false},
		{"unrelated session rejected", authContext{sessionID: "thrawn", authenticated: true, role: roleSession}, "strath", true},
		{"a different orchestrator cannot manage another's scenario", authContext{sessionID: "ben-two", authenticated: true, role: roleOrchestrator}, "strath", true},
		{"read-only guest rejected", authContext{role: roleRemoteGuest, deviceID: "dreich"}, "strath", true},
		{"unpaired remote rejected", authContext{role: roleNone}, "strath", true},
		{"unknown scenario rejected", authContext{sessionID: "ben", authenticated: true, role: roleOrchestrator}, "haar", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.auth.checkScenarioOp(sm, tt.scenario)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkScenarioOp(%q) err = %v, wantErr %v", tt.scenario, err, tt.wantErr)
			}
		})
	}
}

func TestIsOrchestrator_Unauthenticated(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben": {ID: "ben", SystemKind: SystemKindOrchestrator},
	})
	// An unauthenticated (human) caller is never the orchestrator session, even
	// if it carries an orchestrator session ID.
	if (authContext{sessionID: "ben"}).isOrchestrator(sm) {
		t.Error("unauthenticated context must not be treated as orchestrator")
	}
}

func TestIsOrchestrator_SessionNotInState(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben": {ID: "ben", SystemKind: SystemKindOrchestrator},
	})
	// A token that resolved to a session ID no longer present in state must
	// fail closed rather than panic or grant orchestrator rights.
	if (authContext{sessionID: "haar", authenticated: true}).isOrchestrator(sm) {
		t.Error("a session missing from state must not be treated as orchestrator")
	}
}

func TestIsDescendantOf_CycleTerminates(t *testing.T) {
	// A corrupted parent cycle (loch → ben → loch) must not loop forever; the
	// visited-set guard returns false once a node repeats.
	sm := newTestSMWithSessions(map[string]*SessionState{
		"loch": {ID: "loch", ParentID: "ben"},
		"ben":  {ID: "ben", ParentID: "loch"},
	})
	if sm.isDescendantOf("loch", "ghost") {
		t.Error("cycle must terminate and report not-a-descendant of an absent root")
	}
}

func TestResolveAuth_OrchestratorRole(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben": {ID: "ben", Token: "tok-ben", SystemKind: SystemKindOrchestrator},
	})

	auth, err := resolveAuth(sm, "tok-ben", ConnOrigin{}, "")
	if err != nil {
		t.Fatal(err)
	}

	if auth.role != roleOrchestrator {
		t.Errorf("role = %d, want roleOrchestrator", auth.role)
	}
}
