package protocol

import (
	"testing"
	"time"
)

func TestEncodeDecodeControl(t *testing.T) {
	handshake := HandshakeMsg{
		Version: "1.0", ClientID: "brig-client",
		TerminalSize: [2]uint16{80, 24}, Cwd: "/home/user/croft",
	}

	data, err := EncodeControl("handshake", handshake)
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}

	if msg.Type != "handshake" {
		t.Errorf("Type = %q, want handshake", msg.Type)
	}

	var got HandshakeMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.ClientID != "brig-client" {
		t.Errorf("ClientID = %q", got.ClientID)
	}

	if got.Cwd != "/home/user/croft" {
		t.Errorf("Cwd = %q", got.Cwd)
	}
}

func TestVersionCompatible(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"same version", Version, true},
		{"same major different minor", "1.99", true},
		{"different major", "2.0", false},
		{"no dot", "1", false},
		{"empty string", "", false},
		{"major only with dot", "1.", true},
		{"three part version", "1.2.3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VersionCompatible(tt.version); got != tt.want {
				t.Errorf("VersionCompatible(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestSessionInfoRoundTrip(t *testing.T) {
	session := SessionInfo{
		ID: "a3f2b1c9", Name: "braw-auth-fix", RepoPath: "/home/user/croft",
		RepoName: "croft", Branch: "d0ugal/graith/braw-auth-fix-a3f2b1c9",
		Agent: "claude", Status: "running",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := EncodeControl("session_update", session)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got SessionInfo
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}

	if got.ID != "a3f2b1c9" || got.Name != "braw-auth-fix" {
		t.Errorf("session = %+v", got)
	}
}
