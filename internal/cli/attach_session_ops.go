package cli

import (
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
)

// freshClient opens a new passive daemon connection. Each overlay/shell/switch
// transition in the attach loop needs a fresh connection because RunPassthrough
// closes the one it was handed. It is a package var returning the attachConn
// interface so the attach-loop handlers can be exercised against a scripted
// fake in tests; production always dials a real *client.Client.
var freshClient = func() (attachConn, error) {
	return client.ConnectPassive(cfg, paths, cfgFile)
}

// fetchScrollback and runScrollView are seams for the scroll-mode handler.
// Production uses the client implementations; tests replace them so the
// handler can be exercised without dialing a daemon or launching a TUI.
var (
	fetchScrollback = client.FetchScrollback
	runScrollView   = client.RunScrollView
)

func previewFetcher() func(string) string {
	return func(sessionID string) string {
		return client.FetchScrollbackPreview(cfg, paths, cfgFile, sessionID)
	}
}

// refreshSessions opens a fresh connection and fetches the session list (msg
// selects live vs deleted), returning nil on any connection or read failure.
// Shared by the overlay's live and Deleted-view refreshers.
func refreshSessions(msg any) []protocol.SessionInfo {
	c, closeConn, err := newControlConn()
	if err != nil {
		return nil
	}
	defer closeConn()

	list, err := fetchSessionList(c, msg)
	if err != nil {
		return nil
	}

	return list.Sessions
}

func sessionRefresher() func() []protocol.SessionInfo {
	return func() []protocol.SessionInfo {
		return refreshSessions(struct{}{})
	}
}

// deletedRefresher fetches the soft-deleted sessions for the overlay's Deleted
// view (a live-list refresh with ListMsg{Deleted:true}).
func deletedRefresher() func() []protocol.SessionInfo {
	return func() []protocol.SessionInfo {
		return refreshSessions(protocol.ListMsg{Deleted: true})
	}
}

func conversationFetcher(sessionID string) func() ([]protocol.ConversationMessage, bool) {
	return func() ([]protocol.ConversationMessage, bool) {
		msgs, err := client.FetchConversation(cfg, paths, cfgFile, sessionID)
		if err != nil {
			return nil, false
		}

		return msgs, true
	}
}

// newControlConn dials a fresh daemon connection for a one-shot control
// operation, returning the connection and its closer. It is a package var so
// the one-shot session operations can be unit-tested against a scripted fake
// without a live daemon; production always uses freshClient.
var newControlConn = func() (controlConn, func(), error) {
	c, err := freshClient()
	if err != nil {
		return nil, nil, err
	}

	return c, c.Close, nil
}

// withFreshClient opens a fresh connection and runs a single control operation
// against it, closing the connection afterwards. It is the shared skeleton for
// the one-shot session operations the overlay drives (star, stop, restart,
// rename, delete, restore).
func withFreshClient(op func(controlConn) error) error {
	c, closeConn, err := newControlConn()
	if err != nil {
		return err
	}
	defer closeConn()

	return op(c)
}

func toggleStar(sessionID string, star bool) error {
	return withFreshClient(func(c controlConn) error {
		if star {
			return controlOp(c, "star", protocol.StarMsg{SessionID: sessionID})
		}

		return controlOp(c, "unstar", protocol.UnstarMsg{SessionID: sessionID})
	})
}

func restartSession(sessionID string) error {
	return withFreshClient(func(c controlConn) error {
		return controlOp(c, "restart", protocol.RestartMsg{SessionID: sessionID})
	})
}

func stopSession(sessionID string) error {
	return withFreshClient(func(c controlConn) error {
		return controlOp(c, "stop", protocol.StopMsg{SessionID: sessionID})
	})
}

func renameSession(sessionID, newName string) error {
	if err := daemon.ValidateSessionName(newName); err != nil {
		return err
	}

	return withFreshClient(func(c controlConn) error {
		return controlOp(c, "rename", protocol.RenameMsg{SessionID: sessionID, NewName: newName})
	})
}

func deleteSession(sessionID string) error {
	return withFreshClient(func(c controlConn) error {
		return controlOp(c, "delete", protocol.DeleteMsg{SessionID: sessionID})
	})
}

// restoreSession un-deletes a soft-deleted session (the overlay Deleted view's
// only action).
func restoreSession(sessionID string) error {
	return withFreshClient(func(c controlConn) error {
		return controlOp(c, "restore", protocol.RestoreMsg{SessionID: sessionID})
	})
}
