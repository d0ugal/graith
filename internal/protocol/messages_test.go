package protocol

import (
	"strings"
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

func TestMsgPubNoReplyRoundTrip(t *testing.T) {
	want := MsgPubMsg{
		Stream: "updates", Body: "morning briefing complete",
		SenderID: "braw-sender", SenderName: "Braw", NoReply: true,
	}

	data, err := EncodeControl("msg_pub", want)
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}

	env, err := DecodeControl(data)
	if err != nil {
		t.Fatalf("DecodeControl: %v", err)
	}

	var got MsgPubMsg
	if err := DecodePayload(env, &got); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}

	if !got.NoReply {
		t.Errorf("NoReply = false, want true")
	}

	defaultData, err := EncodeControl("msg_pub", MsgPubMsg{Stream: "updates", Body: "replyable"})
	if err != nil {
		t.Fatalf("EncodeControl default: %v", err)
	}

	if strings.Contains(string(defaultData), "no_reply") {
		t.Errorf("default no_reply should be omitted for backward compatibility: %s", defaultData)
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

func TestPairRequestRoundTrip(t *testing.T) {
	req := PairRequestMsg{DeviceLabel: "bairn", DevicePubKey: "ed25519-pubkey-abc"}

	data, err := EncodeControl("pair_request", req)
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}

	if msg.Type != "pair_request" {
		t.Errorf("Type = %q, want pair_request", msg.Type)
	}

	var got PairRequestMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.DeviceLabel != "bairn" || got.DevicePubKey != "ed25519-pubkey-abc" {
		t.Errorf("pair request = %+v", got)
	}
}

func TestPairResponseRoundTrip(t *testing.T) {
	resp := PairResponseMsg{
		DeviceID: "dev-skelf", ClientToken: "tok-croft",
		DaemonProfile: "default", TLSPinSPKI: "spki-pin-xyz",
	}

	data, err := EncodeControl("pair_response", resp)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got PairResponseMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.DeviceID != "dev-skelf" || got.ClientToken != "tok-croft" ||
		got.DaemonProfile != "default" || got.TLSPinSPKI != "spki-pin-xyz" {
		t.Errorf("pair response = %+v", got)
	}
}

func TestPairApproveRoundTrip(t *testing.T) {
	approve := PairApproveMsg{RequestID: "req-speir"}

	data, err := EncodeControl("pair_approve", approve)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got PairApproveMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.RequestID != "req-speir" {
		t.Errorf("pair approve = %+v", got)
	}
}

func TestPairListResponseRoundTrip(t *testing.T) {
	resp := PairListResponseMsg{
		Pending: []PairPending{{
			RequestID: "req-bairn", DeviceLabel: "bairn",
			TailnetUser: "speir@example.com", TailnetNode: "node-croft",
			RequestedAt: "2026-07-07T10:00:00Z",
		}},
		Paired: []PairedDeviceInfo{{
			DeviceID: "dev-skelf", Label: "skelf",
			TailnetUser: "speir@example.com", TailnetNode: "node-bothy",
			CreatedAt: "2026-07-06T09:00:00Z", LastSeenAt: "2026-07-07T09:00:00Z",
		}},
	}

	data, err := EncodeControl("pair_list_response", resp)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got PairListResponseMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Pending) != 1 || got.Pending[0].DeviceLabel != "bairn" ||
		got.Pending[0].TailnetUser != "speir@example.com" {
		t.Errorf("pending = %+v", got.Pending)
	}

	if len(got.Paired) != 1 || got.Paired[0].Label != "skelf" ||
		got.Paired[0].LastSeenAt != "2026-07-07T09:00:00Z" {
		t.Errorf("paired = %+v", got.Paired)
	}
}

func TestPairRevokeRoundTrip(t *testing.T) {
	revoke := PairRevokeMsg{DeviceID: "dev-thrawn"}

	data, err := EncodeControl("pair_revoke", revoke)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got PairRevokeMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.DeviceID != "dev-thrawn" {
		t.Errorf("pair revoke = %+v", got)
	}
}

func TestAuthChallengeRoundTrip(t *testing.T) {
	chal := AuthChallengeMsg{Nonce: "nonce-haar"}

	data, err := EncodeControl("auth_challenge", chal)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got AuthChallengeMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.Nonce != "nonce-haar" {
		t.Errorf("auth challenge = %+v", got)
	}
}

func TestAuthProofRoundTrip(t *testing.T) {
	proof := AuthProofMsg{DeviceID: "dev-skelf", Signature: "sig-bairn"}

	data, err := EncodeControl("auth_proof", proof)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got AuthProofMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.DeviceID != "dev-skelf" || got.Signature != "sig-bairn" {
		t.Errorf("auth proof = %+v", got)
	}
}

func TestApprovalSubscribeRoundTrip(t *testing.T) {
	data, err := EncodeControl("approval_subscribe", ApprovalSubscribeMsg{})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}

	if msg.Type != "approval_subscribe" {
		t.Errorf("Type = %q, want approval_subscribe", msg.Type)
	}

	var got ApprovalSubscribeMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}
}

func TestRepoListResponseRoundTrip(t *testing.T) {
	resp := RepoListResponseMsg{
		Repos: []RepoEntry{
			{Path: "/home/user/croft", Name: "croft", Recent: true},
			{Path: "/home/user/bothy", Name: "bothy"},
		},
	}

	data, err := EncodeControl("repo_list_response", resp)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got RepoListResponseMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Repos) != 2 {
		t.Fatalf("repos = %+v", got.Repos)
	}

	if got.Repos[0].Name != "croft" || !got.Repos[0].Recent {
		t.Errorf("repo[0] = %+v", got.Repos[0])
	}

	if got.Repos[1].Name != "bothy" || got.Repos[1].Recent {
		t.Errorf("repo[1] = %+v", got.Repos[1])
	}
}
