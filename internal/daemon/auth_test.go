package daemon

import (
	"sync"
	"testing"
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

func TestResolveAuth_NoToken(t *testing.T) {
	sm := newTestSMWithSessions(nil)
	auth, err := resolveAuth(sm, "")
	if err != nil {
		t.Fatal(err)
	}
	if auth.authenticated {
		t.Error("expected unauthenticated for empty token")
	}
	if auth.sessionID != "" {
		t.Error("expected empty session ID")
	}
}

func TestResolveAuth_ValidToken(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw": {ID: "braw", Token: "tok-braw"},
	})
	auth, err := resolveAuth(sm, "tok-braw")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.authenticated {
		t.Error("expected authenticated")
	}
	if auth.sessionID != "braw" {
		t.Errorf("session = %q, want braw", auth.sessionID)
	}
}

func TestResolveAuth_InvalidToken(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw": {ID: "braw", Token: "tok-braw"},
	})
	_, err := resolveAuth(sm, "tok-thrawn")
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

func TestCheckMsgPub_TopicAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw": {ID: "braw"},
	})
	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkMsgPub(sm, "blether-review"); err != nil {
		t.Errorf("expected allowed for topic publish, got: %v", err)
	}
}

func TestCheckMsgPub_InboxSelfAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw": {ID: "braw"},
	})
	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkMsgPub(sm, "inbox:braw"); err != nil {
		t.Errorf("expected allowed for own inbox, got: %v", err)
	}
}

func TestCheckMsgPub_InboxChildAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben":   {ID: "ben"},
		"bairn": {ID: "bairn", ParentID: "ben"},
	})
	auth := authContext{sessionID: "ben", authenticated: true}
	if err := auth.checkMsgPub(sm, "inbox:bairn"); err != nil {
		t.Errorf("expected allowed for child inbox, got: %v", err)
	}
}

func TestCheckMsgPub_InboxParentAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben":   {ID: "ben"},
		"bairn": {ID: "bairn", ParentID: "ben"},
	})
	auth := authContext{sessionID: "bairn", authenticated: true}
	if err := auth.checkMsgPub(sm, "inbox:ben"); err != nil {
		t.Errorf("expected allowed for parent inbox, got: %v", err)
	}
}

func TestCheckMsgPub_InboxUnrelatedAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"braw":  {ID: "braw"},
		"canny": {ID: "canny"},
	})
	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkMsgPub(sm, "inbox:canny"); err != nil {
		t.Errorf("expected allowed for unrelated inbox, got: %v", err)
	}
}

func TestCheckInboxRead_OwnInboxAllowed(t *testing.T) {
	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkInboxRead("inbox:braw"); err != nil {
		t.Errorf("expected allowed for own inbox, got: %v", err)
	}
}

func TestCheckInboxRead_OtherInboxRejected(t *testing.T) {
	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkInboxRead("inbox:canny"); err == nil {
		t.Error("expected rejection for other inbox read")
	}
}

func TestCheckInboxRead_TopicAllowed(t *testing.T) {
	auth := authContext{sessionID: "braw", authenticated: true}
	if err := auth.checkInboxRead("blether-review"); err != nil {
		t.Errorf("expected allowed for topic read, got: %v", err)
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
