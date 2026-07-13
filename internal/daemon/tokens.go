package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/protocol"
)

const (
	// tokenPollInterval is the cadence at which RunTokenLoop re-derives per-session
	// token usage from transcripts. A constant in v1 (no config knob); the
	// fingerprint cache means an idle fleet does almost no work between ticks.
	tokenPollInterval = 30 * time.Second
	// tokenStartupDelay is the short first-tick delay so `gr tokens` isn't blank
	// for a full interval after a daemon (re)start.
	tokenStartupDelay = 5 * time.Second
	// tokenBatchCap bounds how many sessions are (re)parsed per tick so a large
	// fleet with big transcripts can't stall the loop.
	tokenBatchCap = 8
)

// tokenCacheEntry caches the last parse for a session, keyed by a fingerprint of
// its source files so an unchanged transcript is skipped without re-reading.
type tokenCacheEntry struct {
	fingerprint string
	stats       *TokenStats
}

// tokenCache is the in-memory, non-persisted parse cache for RunTokenLoop.
type tokenCache struct {
	mu      sync.Mutex
	entries map[string]tokenCacheEntry
}

func newTokenCache() *tokenCache {
	return &tokenCache{entries: make(map[string]tokenCacheEntry)}
}

func (c *tokenCache) get(id string) (tokenCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[id]

	return e, ok
}

func (c *tokenCache) put(id string, e tokenCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[id] = e
}

// prune drops cache entries for sessions no longer present (purged), bounding
// growth over a long-running daemon.
func (c *tokenCache) prune(live map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for id := range c.entries {
		if !live[id] {
			delete(c.entries, id)
		}
	}
}

// tokenTarget is a snapshot of the fields RunTokenLoop needs to resolve and
// parse a session's transcript, taken under RLock and used off-lock.
type tokenTarget struct {
	id             string
	agent          string
	agentSessionID string
	worktreePath   string
}

// RunTokenLoop periodically re-derives per-session token usage from each
// supported session's on-disk transcript, writing runtime-only TokenStats onto
// SessionState (never persisted, repopulated within a tick after restart).
func (sm *SessionManager) RunTokenLoop(ctx context.Context) {
	timer := time.NewTimer(tokenStartupDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		sm.runTokenTick(ctx)

		timer.Reset(tokenPollInterval)
	}
}

func (sm *SessionManager) runTokenTick(ctx context.Context) {
	targets, live := sm.tokenTargets()

	sm.tokens.prune(live)

	parsed := 0

	for _, t := range targets {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if parsed >= tokenBatchCap {
			break
		}

		if sm.pollTokens(t) {
			parsed++
		}
	}
}

// tokenTargets snapshots eligible sessions and the set of all live session ids
// (for cache pruning). Eligible = running, stopped, or errored (all can have
// billable usage), agent has a usage reader. Unlike prWatchTargets, mirror and
// in-place sessions are NOT excluded — each runs its own agent with its own
// transcript, so its tokens are real. Soft-deleted sessions are skipped for
// polling but keep their last-known stats (not cleared here).
func (sm *SessionManager) tokenTargets() ([]tokenTarget, map[string]bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	live := make(map[string]bool, len(sm.state.Sessions))

	var targets []tokenTarget

	for id, s := range sm.state.Sessions {
		live[id] = true

		if s.IsSoftDeleted() {
			continue
		}

		switch s.Status {
		case StatusRunning, StatusStopped, StatusErrored:
		default:
			continue
		}

		if !transcript.Supported(s.Agent) {
			continue
		}

		targets = append(targets, tokenTarget{
			id:             id,
			agent:          s.Agent,
			agentSessionID: s.AgentSessionID,
			worktreePath:   s.WorktreePath,
		})
	}

	return targets, live
}

// pollTokens resolves, fingerprints, and (if changed) re-parses one session's
// transcript, writing the result onto SessionState. It returns true when it
// actually parsed (i.e. counted against the batch cap); a fingerprint cache hit
// or an unreadable transcript returns false. Runs OFF sm.mu (it touches the
// filesystem); only the final write-back takes the lock.
func (sm *SessionManager) pollTokens(t tokenTarget) bool {
	sources, err := transcript.Locate(t.agent, t.agentSessionID, t.worktreePath)
	if err != nil || len(sources) == 0 {
		// No transcript yet (or unreadable): leave any previous stats in place
		// rather than clearing a known total on a transient miss.
		return false
	}

	fp := tokenFingerprint(sources)

	if e, ok := sm.tokens.get(t.id); ok && e.fingerprint == fp {
		return false // unchanged since last successful parse
	}

	u, err := transcript.UsageFrom(t.agent, sources)
	if err != nil {
		return false
	}

	if !u.Found {
		// Parsed cleanly but no usage records — cache the fingerprint so we don't
		// re-parse an unchanged empty transcript, but don't publish a stats value
		// (unknown, not a confident zero).
		sm.tokens.put(t.id, tokenCacheEntry{fingerprint: fp})

		return true
	}

	stats := &TokenStats{
		Input:         u.Input,
		Output:        u.Output,
		CacheCreation: u.CacheCreation,
		CacheRead:     u.CacheRead,
		Unclassified:  u.Unclassified,
		Total:         u.Total(),
		Degraded:      u.Dropped > 0,
		CountedAt:     time.Now(),
	}

	sm.tokens.put(t.id, tokenCacheEntry{fingerprint: fp, stats: stats})
	sm.setTokenStats(t.id, stats)

	return true
}

// setTokenStats publishes freshly-built stats onto a session under the lock.
// The pointer is never mutated after publication, so off-lock clones are safe.
func (sm *SessionManager) setTokenStats(id string, stats *TokenStats) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if s, ok := sm.state.Sessions[id]; ok {
		s.Tokens = stats
	}
}

// tokenFingerprint hashes the source files' size+mtime (not mtime alone) so a
// grown, rotated, or re-pointed transcript forces a re-read while an untouched
// one is skipped.
func tokenFingerprint(sources []transcript.Source) string {
	var b []byte
	for _, s := range sources {
		b = append(b, s.Path...)
		b = append(b, byte(0))
		b = appendInt(b, s.Size)
		b = append(b, byte(0))
		b = appendInt(b, s.ModTime.UnixNano())
		b = append(b, byte('|'))
	}

	return string(b)
}

func appendInt(b []byte, n int64) []byte {
	if n == 0 {
		return append(b, '0')
	}

	if n < 0 {
		b = append(b, '-')
		n = -n
	}

	var tmp [20]byte

	i := len(tmp)
	for n > 0 {
		i--
		tmp[i] = byte('0' + n%10)
		n /= 10
	}

	return append(b, tmp[i:]...)
}

// tokenInfo projects runtime TokenStats to the protocol type, or nil when
// unknown (never observed). Kept beside the token loop so token code is together.
func tokenInfo(t *TokenStats) *protocol.TokenInfo {
	if t == nil {
		return nil
	}

	info := &protocol.TokenInfo{
		Input:         t.Input,
		Output:        t.Output,
		CacheCreation: t.CacheCreation,
		CacheRead:     t.CacheRead,
		Unclassified:  t.Unclassified,
		Total:         t.Total,
		Degraded:      t.Degraded,
	}
	if !t.CountedAt.IsZero() {
		info.CountedAt = t.CountedAt.Format(time.RFC3339)
	}

	return info
}
