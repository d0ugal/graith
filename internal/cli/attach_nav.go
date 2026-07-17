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
// re-probe the daemon every 250ms for up to 10s before giving up. They are
// package vars, not consts, so ConfigureReconnect can install the [connection]
// config overrides at CLI startup (issue #1242); the initial values reproduce
// the pre-config behaviour.
var (
	reconnectDeadline = 10 * time.Second
	reconnectInterval = 250 * time.Millisecond
)

// ConfigureReconnect installs the configured attach reconnect deadline and
// re-probe interval. It is called once from the CLI pre-run after config load. A
// non-positive value is ignored so a change can't silently disable recovery;
// callers pass values already defaulted by the config accessors.
func ConfigureReconnect(deadline, interval time.Duration) {
	if deadline > 0 {
		reconnectDeadline = deadline
	}

	if interval > 0 {
		reconnectInterval = interval
	}
}

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
	for {
		// Cap the pre-dial sleep to the remaining budget so a reconnect_interval
		// larger than the aggregate reconnect_timeout can't overshoot the
		// documented window (issue #1242). When no budget remains, stop.
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			break
		}

		nap := interval
		if nap > remaining {
			nap = remaining
		}

		sleep(nap)

		// Re-check the budget after sleeping and stop BEFORE dialing once it is
		// exhausted: a dial + handshake started at (or past) the aggregate
		// deadline carries its own distinct dial/handshake budget and would run
		// well beyond reconnect_timeout, still violating the aggregate cap
		// (issue #1242). Every actual attempt therefore starts strictly before
		// the deadline.
		if !now().Before(deadline) {
			break
		}

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
