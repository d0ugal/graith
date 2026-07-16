package cli

import (
	"fmt"
	"sort"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
)

// sortedSessionIDs returns session IDs in the same order as the overlay:
// grouped by repo (alphabetically), then within each group sorted by
// running status first, then alphabetically by name.
func sortedSessionIDs(sessions []protocol.SessionInfo) []string {
	groups := map[string][]protocol.SessionInfo{}

	var repoOrder []string

	seen := map[string]bool{}

	for _, s := range sessions {
		repo := s.RepoName
		if repo == "" {
			repo = "(no repo)"
		}

		if !seen[repo] {
			repoOrder = append(repoOrder, repo)
			seen[repo] = true
		}

		groups[repo] = append(groups[repo], s)
	}

	sort.Strings(repoOrder)

	for _, g := range groups {
		client.SortSessions(g)
	}

	var ids []string

	for _, repo := range repoOrder {
		for _, s := range groups[repo] {
			ids = append(ids, s.ID)
		}
	}

	return ids
}

func adjacentSession(ids []string, current string, forward bool) string {
	if len(ids) < 2 {
		return ""
	}

	for i, id := range ids {
		if id == current {
			if forward {
				return ids[(i+1)%len(ids)]
			}

			return ids[(i-1+len(ids))%len(ids)]
		}
	}

	return ""
}

// reconnectConn is a controlConn that can also be closed. reconnectLoop dials
// these so it can be unit-tested against a scripted fake instead of a live
// daemon socket.
type reconnectConn interface {
	controlConn
	Close()
}

// reconnectDeadline / reconnectInterval bound the disconnect-recovery retry:
// re-probe the daemon every 250ms for up to 10s before giving up.
const (
	reconnectDeadline = 10 * time.Second
	reconnectInterval = 250 * time.Millisecond
)

// reconnectLoop retries dialing the daemon and re-attaching to sessionID until
// it succeeds, the daemon reports the session is gone, or the timeout elapses.
// now/sleep/dial are injected so the retry cadence can be exercised
// deterministically in tests without real time passing.
func reconnectLoop(
	sessionID string,
	dial func() (reconnectConn, error),
	now func() time.Time,
	sleep func(time.Duration),
	timeout, interval time.Duration,
) (reconnectConn, protocol.Envelope, error) {
	deadline := now().Add(timeout)
	for now().Before(deadline) {
		sleep(interval)

		c, err := dial()
		if err != nil {
			continue
		}

		_ = c.SendControl("attach", attachMsg(sessionID))

		resp, err := c.ReadControlResponse()
		if err != nil {
			c.Close()
			continue
		}

		if resp.Type == "error" {
			c.Close()

			return nil, protocol.Envelope{}, fmt.Errorf("session unavailable: %s", errorMessage(resp))
		}

		return c, resp, nil
	}

	return nil, protocol.Envelope{}, fmt.Errorf("timed out after %s", timeout)
}

// reconnectToSession wraps reconnectLoop with the production seams: a real
// daemon dial and wall-clock timing.
func reconnectToSession(sessionID string) (*client.Client, protocol.Envelope, error) {
	c, resp, err := reconnectLoop(
		sessionID,
		func() (reconnectConn, error) { return freshClient() },
		time.Now,
		time.Sleep,
		reconnectDeadline,
		reconnectInterval,
	)
	if err != nil {
		return nil, protocol.Envelope{}, err
	}

	return c.(*client.Client), resp, nil
}
