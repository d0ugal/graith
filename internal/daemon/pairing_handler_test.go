package daemon

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// remoteConn drives HandleConnection with a remote (tailnet) origin over an
// in-memory pipe, for exercising the pairing / proof-of-possession / Gate-A
// flow end to end at the handler level.
type remoteConn struct {
	reader *protocol.FrameReader
	writer *protocol.FrameWriter
}

func newRemoteConn(t *testing.T, sm *SessionManager, identity TailnetIdentity) *remoteConn {
	t.Helper()

	client, server := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		HandleConnection(ctx, server, ConnOrigin{Remote: true, Identity: &identity}, sm, sm.log)
	}()

	t.Cleanup(func() {
		cancel()

		_ = client.Close()
		_ = server.Close()

		<-done
	})

	return &remoteConn{
		reader: protocol.NewFrameReader(client),
		writer: protocol.NewFrameWriter(client),
	}
}

func (rc *remoteConn) send(t *testing.T, msgType string, payload any, token string) {
	t.Helper()

	var (
		data []byte
		err  error
	)

	if token != "" {
		data, err = protocol.EncodeControlWithToken(msgType, payload, token)
	} else {
		data, err = protocol.EncodeControl(msgType, payload)
	}

	if err != nil {
		t.Fatal(err)
	}

	if err := rc.writer.WriteFrame(protocol.ChannelControl, data); err != nil {
		t.Fatal(err)
	}
}

func (rc *remoteConn) read(t *testing.T) protocol.Envelope {
	t.Helper()

	for {
		frame, err := rc.reader.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}

		if frame.Channel == protocol.ChannelControl {
			env, err := protocol.DecodeControl(frame.Payload)
			if err != nil {
				t.Fatal(err)
			}

			return env
		}
	}
}

func (rc *remoteConn) handshake(t *testing.T, sm *SessionManager) protocol.AuthChallengeMsg {
	t.Helper()

	rc.send(t, "handshake", protocol.HandshakeMsg{Version: protocol.Version, Profile: sm.paths.Profile}, "")

	if env := rc.read(t); env.Type != "handshake_ok" {
		t.Fatalf("expected handshake_ok, got %q", env.Type)
	}

	// Remote connections then receive a PoP challenge.
	env := rc.read(t)
	if env.Type != "auth_challenge" {
		t.Fatalf("expected auth_challenge, got %q", env.Type)
	}

	var ch protocol.AuthChallengeMsg
	if err := protocol.DecodePayload(env, &ch); err != nil {
		t.Fatal(err)
	}

	return ch
}

// TestPairRequestDeliversTokenOnApproval covers the blocking pair_request flow:
// a new remote device requests pairing, a local human approves it, and the
// device receives the minted token over its held-open connection.
func TestPairRequestDeliversTokenOnApproval(t *testing.T) {
	sm := newPairingSM(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	rc := newRemoteConn(t, sm, TailnetIdentity{User: "speir@example.com", Node: "ben"})
	rc.handshake(t, sm) // consumes handshake_ok + auth_challenge

	rc.send(t, "pair_request", protocol.PairRequestMsg{DeviceLabel: "bairn", DevicePubKey: pubB64}, "")

	// The handler is now blocked awaiting approval. Find the pending request and
	// approve it locally, which delivers the credentials to the blocked conn.
	var rid string

	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		if pending, _ := sm.ListPairings(); len(pending) == 1 {
			rid = pending[0].RequestID
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	if rid == "" {
		t.Fatal("pending pairing never appeared")
	}

	wantDevice, wantToken, err := sm.ApprovePairing(rid, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	env := rc.read(t)
	if env.Type != "pair_response" {
		t.Fatalf("expected pair_response, got %q", env.Type)
	}

	var pr protocol.PairResponseMsg
	if err := protocol.DecodePayload(env, &pr); err != nil {
		t.Fatal(err)
	}

	if pr.DeviceID != wantDevice || pr.ClientToken != wantToken {
		t.Errorf("pair_response = {%s,%s}, want {%s,%s}", pr.DeviceID, pr.ClientToken, wantDevice, wantToken)
	}

	if pr.ClientToken == "" {
		t.Error("pair_response carried an empty client token")
	}
}

// TestProofOfPossessionUnlocksRemoteHuman covers the PoP handshake: a paired
// device signs the daemon's challenge, and only then may it reach a
// roleRemoteHuman message like `list`.
func TestProofOfPossessionUnlocksRemoteHuman(t *testing.T) {
	sm := newPairingSM(t)
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	id := TailnetIdentity{User: "speir@example.com", Node: "ben"}

	rid, _, _, err := sm.AddPendingPairing("bairn", pubB64, id, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	_, token, err := sm.ApprovePairing(rid, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	rc := newRemoteConn(t, sm, id)
	ch := rc.handshake(t, sm)

	// Before PoP, a privileged message is rejected by Gate A.
	rc.send(t, "list", struct{}{}, token)

	if env := rc.read(t); env.Type != "error" {
		t.Fatalf("list before PoP: expected error, got %q", env.Type)
	}

	// Sign the challenge (bound to the daemon's TLS pin, issue #886) and present
	// proof.
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, protocol.PoPSigningInput(ch.Nonce, sm.remoteTLSPin)))
	rc.send(t, "auth_proof", protocol.AuthProofMsg{Signature: sig}, token)

	if env := rc.read(t); env.Type != "auth_ok" {
		t.Fatalf("expected auth_ok after valid proof, got %q", env.Type)
	}

	// Now `list` succeeds (roleRemoteHuman).
	rc.send(t, "list", struct{}{}, token)

	if env := rc.read(t); env.Type != "session_list" {
		t.Fatalf("list after PoP: expected session_list, got %q", env.Type)
	}
}

// TestRemoteRoleNoneGateADenies confirms an unpaired remote connection cannot
// reach anything outside the pairing lane, and that pairing approval is
// local-only.
func TestRemoteRoleNoneGateADenies(t *testing.T) {
	sm := newPairingSM(t)
	rc := newRemoteConn(t, sm, TailnetIdentity{User: "speir@example.com", Node: "ben"})
	rc.handshake(t, sm)

	// roleNone remote: list is denied.
	rc.send(t, "list", struct{}{}, "")

	if env := rc.read(t); env.Type != "error" {
		t.Errorf("roleNone list: expected error, got %q", env.Type)
	}

	// pair_approve is local-only — denied over remote even before auth.
	rc.send(t, "pair_approve", protocol.PairApproveMsg{RequestID: "whatever"}, "")

	if env := rc.read(t); env.Type != "error" {
		t.Errorf("remote pair_approve: expected error, got %q", env.Type)
	}
}

// TestRemoteHandshakePairingUnaffectedByHumanToken checks that provisioning a
// human token — which a served daemon always has, and which is what triggers
// PR #1066's local handshake auth-gate exemption — does not disturb the remote
// pairing handshake: a remote tokenless handshake still gets handshake_ok +
// auth_challenge, exactly as before.
//
// Note this asserts the observable remote wire behaviour, not the underlying
// reason it is safe. The handshake exemption at handler.go
// (`authErr != nil && msg.Type != "handshake"`) applies regardless of origin, so
// it is a no-op for remote only because resolveAuth never returns a non-nil error
// for a remote connection. That narrower invariant is locked directly by
// TestResolveAuth_RemoteNoPoPIsRoleNone (and the remote-mismatch cases) in
// auth_test.go, which assert err == nil for an unpaired remote.
func TestRemoteHandshakePairingUnaffectedByHumanToken(t *testing.T) {
	sm := newPairingSM(t)

	sm.mu.Lock()
	sm.humanToken = "human-thrawn"
	sm.mu.Unlock()

	rc := newRemoteConn(t, sm, TailnetIdentity{User: "speir@example.com", Node: "ben"})

	rc.send(t, "handshake", protocol.HandshakeMsg{Version: protocol.Version, Profile: sm.paths.Profile}, "")

	if env := rc.read(t); env.Type != "handshake_ok" {
		t.Fatalf("remote tokenless handshake with a human token set: expected handshake_ok, got %q", env.Type)
	}

	if env := rc.read(t); env.Type != "auth_challenge" {
		t.Fatalf("remote handshake: expected auth_challenge second frame, got %q", env.Type)
	}
}

func TestApprovalSubscriberReceivesAndUnsubscribes(t *testing.T) {
	sm := newPairingSM(t)
	got := make(chan string, 4)

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close(); _ = c2.Close() }()

	sm.AddApprovalSubscriber(c1, func(msgType string, _ any) { got <- msgType })

	sm.broadcastApprovalNotification()

	select {
	case m := <-got:
		if m != "approval_notification" {
			t.Errorf("subscriber got %q, want approval_notification", m)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the broadcast")
	}

	// After unsubscribing, a further broadcast must not reach it.
	sm.RemoveApprovalSubscriber(c1)
	sm.broadcastApprovalNotification()

	select {
	case <-got:
		t.Error("removed subscriber still received a broadcast")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAvailableReposReturnsSessionRepos(t *testing.T) {
	sm := newPairingSM(t)
	sm.state.Sessions["braw1"] = &SessionState{ID: "braw1", RepoPath: "/glen/croft", RepoName: "croft"}
	sm.state.Sessions["braw2"] = &SessionState{ID: "braw2", RepoPath: "/glen/croft", RepoName: "croft"} // dup path
	sm.state.Sessions["braw3"] = &SessionState{ID: "braw3", RepoPath: "/brae/bothy", RepoName: "bothy"}

	repos := sm.availableRepos()
	if len(repos) != 2 {
		t.Fatalf("availableRepos returned %d, want 2 distinct", len(repos))
	}

	paths := map[string]bool{}
	for _, r := range repos {
		paths[r.Path] = true
		if !r.Recent {
			t.Errorf("session repo %q should be marked recent", r.Path)
		}
	}

	if !paths["/glen/croft"] || !paths["/brae/bothy"] {
		t.Errorf("unexpected repo set: %+v", repos)
	}
}

// mkGitRepo creates dir (and parents) with a .git marker so isGitRepo treats it
// as a repo, and returns the path.
func mkGitRepo(t *testing.T, dir string) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}

	return dir
}

// Regression for #896: a remote client's create picker is populated from
// repo_list, which must include the configured repos — not only repos that
// already have a live session. Before the fix a daemon with no session in a
// configured repo offered an empty/incomplete picker, so the user could not
// pick a repository to create in at all.
//
// It also guards the follow-up: allowed_repo_paths are container roots, so the
// daemon must SCAN them for git repos (like the local picker) rather than
// listing the container itself — otherwise it offers a non-git directory that
// create rejects.
func TestAvailableReposIncludesConfiguredRepos(t *testing.T) {
	sm := newPairingSM(t)

	root := t.TempDir()
	// A container root (allowed_repo_paths) holding two child repos and one
	// non-git dir that must be ignored.
	code := filepath.Join(root, "code")
	bothy := mkGitRepo(t, filepath.Join(code, "bothy"))
	clachan := mkGitRepo(t, filepath.Join(code, "clachan"))
	mkGitRepo(t, filepath.Join(code, "notes")) // git — also discovered

	if err := os.MkdirAll(filepath.Join(code, "empty"), 0o750); err != nil {
		t.Fatal(err) // non-git dir, must be skipped
	}

	// A repo that also has a live session — it must appear once, as recent,
	// keeping its session-derived name.
	croft := mkGitRepo(t, filepath.Join(root, "croft"))
	sm.state.Sessions["braw1"] = &SessionState{ID: "braw1", RepoPath: croft, RepoName: "croft"}

	sm.cfg.AllowedRepoPaths = []string{code}
	// A [[repos]] entry pointing straight at a repo (added directly) plus a
	// duplicate of the session repo (must not appear twice).
	sm.cfg.Repos = []config.RepoConfig{{Path: clachan}, {Path: croft}}

	repos := sm.availableRepos()

	// Key on the resolved path: a scanned repo's entry path is built from the
	// resolved root, while a session repo keeps its stored (unresolved) path —
	// on macOS /var symlinks to /private/var, so the two spellings differ. Both
	// are valid create inputs; the resolved path is the stable comparison key.
	byPath := map[string]protocol.RepoEntry{}

	for _, r := range repos {
		key := config.ResolvePath(r.Path)
		if _, dup := byPath[key]; dup {
			t.Errorf("duplicate repo entry for %q", r.Path)
		}

		byPath[key] = r
	}

	for _, want := range []string{croft, bothy, clachan} {
		if _, ok := byPath[config.ResolvePath(want)]; !ok {
			t.Errorf("expected repo %q in picker, got %+v", want, repos)
		}
	}

	// The container root itself is not a git repo, so it must not be listed.
	if _, bad := byPath[config.ResolvePath(code)]; bad {
		t.Errorf("non-git container %q should not be offered as a repo", code)
	}

	// The empty non-git child must not be listed.
	empty := filepath.Join(code, "empty")
	if _, bad := byPath[config.ResolvePath(empty)]; bad {
		t.Errorf("non-git dir %q should not be offered as a repo", empty)
	}

	// The session repo keeps its recent flag and session-derived name; a repo
	// discovered only from config is not recent and takes its name from the path.
	if r := byPath[config.ResolvePath(croft)]; !r.Recent || r.Name != "croft" {
		t.Errorf("session repo %q: got recent=%v name=%q, want recent=true name=croft", croft, r.Recent, r.Name)
	}

	if r := byPath[config.ResolvePath(bothy)]; r.Recent || r.Name != "bothy" {
		t.Errorf("scanned repo %q: got recent=%v name=%q, want recent=false name=bothy", bothy, r.Recent, r.Name)
	}
}

// TestRemoteGuestReadOnlyEndToEnd: a device paired while require_pairing=false
// (roleRemoteGuest) can observe but not mutate, end to end over the wire.
func TestRemoteGuestReadOnlyEndToEnd(t *testing.T) {
	sm := newPairingSM(t)
	pub, priv, _ := ed25519.GenerateKey(nil)
	id := TailnetIdentity{User: "speir@example.com", Node: "ben"}

	rid, _, _, err := sm.AddPendingPairing("bairn", base64.StdEncoding.EncodeToString(pub), id, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	_, token, err := sm.ApprovePairing(rid, true, time.Now()) // readOnly → guest
	if err != nil {
		t.Fatal(err)
	}

	rc := newRemoteConn(t, sm, id)
	ch := rc.handshake(t, sm)

	rc.send(t, "auth_proof", protocol.AuthProofMsg{Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, protocol.PoPSigningInput(ch.Nonce, sm.remoteTLSPin)))}, token)

	if env := rc.read(t); env.Type != "auth_ok" {
		t.Fatalf("expected auth_ok, got %q", env.Type)
	}

	// Guest CAN read.
	rc.send(t, "list", struct{}{}, token)

	if env := rc.read(t); env.Type != "session_list" {
		t.Fatalf("guest list: expected session_list, got %q", env.Type)
	}

	// Guest CANNOT mutate.
	rc.send(t, "create", protocol.CreateMsg{Name: "braw"}, token)

	if env := rc.read(t); env.Type != "error" {
		t.Fatalf("guest create: expected error, got %q", env.Type)
	}
}

// TestRevokeDropsLiveRemoteConnection: revoking a device force-closes its live
// authenticated connection (design §B.5), not just future ones.
func TestRevokeDropsLiveRemoteConnection(t *testing.T) {
	sm := newPairingSM(t)
	pub, priv, _ := ed25519.GenerateKey(nil)
	id := TailnetIdentity{User: "speir@example.com", Node: "ben"}

	rid, _, _, err := sm.AddPendingPairing("bairn", base64.StdEncoding.EncodeToString(pub), id, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	deviceID, token, err := sm.ApprovePairing(rid, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	rc := newRemoteConn(t, sm, id)
	ch := rc.handshake(t, sm)

	rc.send(t, "auth_proof", protocol.AuthProofMsg{Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, protocol.PoPSigningInput(ch.Nonce, sm.remoteTLSPin)))}, token)

	if env := rc.read(t); env.Type != "auth_ok" {
		t.Fatalf("expected auth_ok, got %q", env.Type)
	}

	// The connection is registered; revoking must force-close it.
	if _, err := sm.RevokeDevice(deviceID); err != nil {
		t.Fatal(err)
	}

	if _, err := rc.reader.ReadFrame(); err == nil {
		t.Error("expected the revoked connection to be force-closed (read should error)")
	}
}
