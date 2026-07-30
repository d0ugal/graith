package daemon

import "github.com/d0ugal/graith/internal/protocol"

func handleEventFollow(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.EventFollowMsg](msg, send, "invalid event_follow message")
	if !ok {
		return
	}

	info, err := sm.FollowEvents(auth, m.ChildSessionID, m.Events)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
		return
	}

	send("event_followed", info)
}

func handleEventUnfollow(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.EventUnfollowMsg](msg, send, "invalid event_unfollow message")
	if !ok {
		return
	}

	info, err := sm.UnfollowEvents(auth, m.ChildSessionID, m.Events)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
		return
	}

	send("event_unfollowed", info)
}

func handleEventFollowing(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	if _, ok := decodePayload[protocol.EventFollowingMsg](msg, send, "invalid event_following message"); !ok {
		return
	}

	if !auth.isLocalHuman() && !auth.authenticated {
		send("error", protocol.ErrorMsg{Message: "not authorized: only the direct parent session or local user may inspect event forwarding"})
		return
	}

	rules, err := sm.EventFollowing(auth)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
		return
	}

	send("event_following", protocol.EventFollowingResponseMsg{Rules: rules})
}
