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
		"s1": {ID: "s1", Token: "tok-aaa"},
	})
	auth, err := resolveAuth(sm, "tok-aaa")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.authenticated {
		t.Error("expected authenticated")
	}
	if auth.sessionID != "s1" {
		t.Errorf("session = %q, want s1", auth.sessionID)
	}
}

func TestResolveAuth_InvalidToken(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"s1": {ID: "s1", Token: "tok-aaa"},
	})
	_, err := resolveAuth(sm, "tok-bad")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestCheckTarget_AlwaysAllowed(t *testing.T) {
	sm := newTestSMWithSessions(nil)
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkTarget(sm, "any", authAlwaysAllowed); err != nil {
		t.Errorf("expected allowed, got: %v", err)
	}
}

func TestCheckTarget_HumanOnly_RejectsAgent(t *testing.T) {
	sm := newTestSMWithSessions(nil)
	auth := authContext{sessionID: "s1", authenticated: true}
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
		"s1": {ID: "s1"},
	})
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkTarget(sm, "s1", authSelfOnly); err != nil {
		t.Errorf("expected allowed for self, got: %v", err)
	}
}

func TestCheckTarget_SelfOnly_RejectsOther(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"s1": {ID: "s1"},
		"s2": {ID: "s2"},
	})
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkTarget(sm, "s2", authSelfOnly); err == nil {
		t.Error("expected rejection for other session")
	}
}

func TestCheckTarget_SelfOrDescendant_AllowsSelf(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"s1": {ID: "s1"},
	})
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkTarget(sm, "s1", authSelfOrDescendant); err != nil {
		t.Errorf("expected allowed for self, got: %v", err)
	}
}

func TestCheckTarget_SelfOrDescendant_AllowsChild(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"parent": {ID: "parent"},
		"child":  {ID: "child", ParentID: "parent"},
	})
	auth := authContext{sessionID: "parent", authenticated: true}
	if err := auth.checkTarget(sm, "child", authSelfOrDescendant); err != nil {
		t.Errorf("expected allowed for child, got: %v", err)
	}
}

func TestCheckTarget_SelfOrDescendant_AllowsGrandchild(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"root":       {ID: "root"},
		"child":      {ID: "child", ParentID: "root"},
		"grandchild": {ID: "grandchild", ParentID: "child"},
	})
	auth := authContext{sessionID: "root", authenticated: true}
	if err := auth.checkTarget(sm, "grandchild", authSelfOrDescendant); err != nil {
		t.Errorf("expected allowed for grandchild, got: %v", err)
	}
}

func TestCheckTarget_SelfOrDescendant_RejectsSibling(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"parent": {ID: "parent"},
		"child1": {ID: "child1", ParentID: "parent"},
		"child2": {ID: "child2", ParentID: "parent"},
	})
	auth := authContext{sessionID: "child1", authenticated: true}
	if err := auth.checkTarget(sm, "child2", authSelfOrDescendant); err == nil {
		t.Error("expected rejection for sibling")
	}
}

func TestCheckTarget_SelfOrDescendant_RejectsParent(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"parent": {ID: "parent"},
		"child":  {ID: "child", ParentID: "parent"},
	})
	auth := authContext{sessionID: "child", authenticated: true}
	if err := auth.checkTarget(sm, "parent", authSelfOrDescendant); err == nil {
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
		"s1": {ID: "s1"},
	})
	auth := authContext{}
	if err := auth.checkTarget(sm, "s1", authSelfOnly); err != nil {
		t.Errorf("expected allowed for unauthenticated (human) with target, got: %v", err)
	}
	if err := auth.checkTarget(sm, "s1", authSelfOrDescendant); err != nil {
		t.Errorf("expected allowed for unauthenticated (human) with target (descendant rule), got: %v", err)
	}
}

func TestCheckMsgPub_TopicAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"s1": {ID: "s1"},
	})
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkMsgPub(sm, "code-review"); err != nil {
		t.Errorf("expected allowed for topic publish, got: %v", err)
	}
}

func TestCheckMsgPub_InboxSelfAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"s1": {ID: "s1"},
	})
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkMsgPub(sm, "inbox:s1"); err != nil {
		t.Errorf("expected allowed for own inbox, got: %v", err)
	}
}

func TestCheckMsgPub_InboxChildAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"parent": {ID: "parent"},
		"child":  {ID: "child", ParentID: "parent"},
	})
	auth := authContext{sessionID: "parent", authenticated: true}
	if err := auth.checkMsgPub(sm, "inbox:child"); err != nil {
		t.Errorf("expected allowed for child inbox, got: %v", err)
	}
}

func TestCheckMsgPub_InboxParentAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"parent": {ID: "parent"},
		"child":  {ID: "child", ParentID: "parent"},
	})
	auth := authContext{sessionID: "child", authenticated: true}
	if err := auth.checkMsgPub(sm, "inbox:parent"); err != nil {
		t.Errorf("expected allowed for parent inbox, got: %v", err)
	}
}

func TestCheckMsgPub_InboxUnrelatedAllowed(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"s1": {ID: "s1"},
		"s2": {ID: "s2"},
	})
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkMsgPub(sm, "inbox:s2"); err != nil {
		t.Errorf("expected allowed for unrelated inbox, got: %v", err)
	}
}

func TestCheckInboxRead_OwnInboxAllowed(t *testing.T) {
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkInboxRead("inbox:s1"); err != nil {
		t.Errorf("expected allowed for own inbox, got: %v", err)
	}
}

func TestCheckInboxRead_OtherInboxRejected(t *testing.T) {
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkInboxRead("inbox:s2"); err == nil {
		t.Error("expected rejection for other inbox read")
	}
}

func TestCheckInboxRead_TopicAllowed(t *testing.T) {
	auth := authContext{sessionID: "s1", authenticated: true}
	if err := auth.checkInboxRead("code-review"); err != nil {
		t.Errorf("expected allowed for topic read, got: %v", err)
	}
}

func TestIsDescendantOf(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"root":       {ID: "root"},
		"child":      {ID: "child", ParentID: "root"},
		"grandchild": {ID: "grandchild", ParentID: "child"},
		"unrelated":  {ID: "unrelated"},
	})

	tests := []struct {
		target, root string
		want         bool
	}{
		{"root", "root", true},
		{"child", "root", true},
		{"grandchild", "root", true},
		{"unrelated", "root", false},
		{"root", "child", false},
		{"unrelated", "child", false},
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
			"s1": {ID: "s1"},
			"s2": {ID: "s2", Token: "existing-token"},
		},
	}
	if err := migrateV9ToV10(state); err != nil {
		t.Fatal(err)
	}
	if state.Sessions["s1"].Token == "" {
		t.Error("s1 should have a token after migration")
	}
	if state.Sessions["s2"].Token != "existing-token" {
		t.Error("s2 should keep its existing token")
	}
}

func TestRebuildTokenIndex(t *testing.T) {
	sm := &SessionManager{
		state: &State{
			Sessions: map[string]*SessionState{
				"s1": {ID: "s1", Token: "tok-1"},
				"s2": {ID: "s2", Token: "tok-2"},
				"s3": {ID: "s3"},
			},
		},
		tokenIndex: make(map[string]string),
	}
	sm.rebuildTokenIndex()

	if sm.tokenIndex["tok-1"] != "s1" {
		t.Errorf("tok-1 → %q, want s1", sm.tokenIndex["tok-1"])
	}
	if sm.tokenIndex["tok-2"] != "s2" {
		t.Errorf("tok-2 → %q, want s2", sm.tokenIndex["tok-2"])
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
		{"inbox:s1", "s1", true},
		{"inbox:", "", true},
		{"code-review", "", false},
		{"inboxes", "", false},
	}
	for _, tt := range tests {
		id, isInbox := parseInboxStream(tt.stream)
		if id != tt.wantID || isInbox != tt.wantInbox {
			t.Errorf("parseInboxStream(%q) = (%q, %v), want (%q, %v)", tt.stream, id, isInbox, tt.wantID, tt.wantInbox)
		}
	}
}
