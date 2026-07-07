package daemon

import (
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

func TestIdentityAllowed(t *testing.T) {
	user := &TailnetIdentity{User: "speir@example.com", Node: "ben"}
	tagged := &TailnetIdentity{Node: "brae", Tags: []string{"tag:graith"}}

	tests := []struct {
		name  string
		allow []string
		id    *TailnetIdentity
		want  bool
	}{
		{"empty allowlist denies user", nil, user, false},
		{"empty allowlist denies tagged", nil, tagged, false},
		{"nil identity denied", []string{"speir@example.com"}, nil, false},
		{"matching user allowed", []string{"speir@example.com"}, user, true},
		{"non-matching user denied", []string{"fash@example.com"}, user, false},
		{"tag entry admits tagged node", []string{"tag:graith"}, tagged, true},
		{"wrong tag denied", []string{"tag:other"}, tagged, false},
		{"tagged node not admitted by a user entry", []string{"speir@example.com"}, tagged, false},
		{"user not admitted by a tag entry", []string{"tag:graith"}, user, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.RemoteConfig{AllowTailnetUsers: tt.allow}
			if got := identityAllowed(cfg, tt.id); got != tt.want {
				t.Errorf("identityAllowed(%v, %+v) = %v, want %v", tt.allow, tt.id, got, tt.want)
			}
		})
	}
}

func TestIdentityFromWhoIs(t *testing.T) {
	if identityFromWhoIs(nil) != nil {
		t.Error("nil WhoIs must map to nil identity")
	}

	w := &apitype.WhoIsResponse{
		UserProfile: &tailcfg.UserProfile{LoginName: "speir@example.com"},
		Node:        &tailcfg.Node{StableID: tailcfg.StableNodeID("nBEN"), Tags: []string{"tag:graith"}},
	}

	id := identityFromWhoIs(w)
	if id == nil {
		t.Fatal("expected an identity")
	}

	if id.User != "speir@example.com" {
		t.Errorf("User = %q, want speir@example.com", id.User)
	}

	if id.Node != "nBEN" {
		t.Errorf("Node = %q, want nBEN", id.Node)
	}

	if len(id.Tags) != 1 || id.Tags[0] != "tag:graith" {
		t.Errorf("Tags = %v, want [tag:graith]", id.Tags)
	}
}

func TestNewRemoteListenerUnknownMode(t *testing.T) {
	if _, err := newRemoteListener(t.Context(), config.RemoteConfig{Mode: "wheesht"}, t.TempDir()); err == nil {
		t.Error("expected error for unknown remote mode")
	}
}
