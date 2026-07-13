package transcript

import (
	"fmt"
	"os"
	"time"
)

// Usage is aggregated token usage parsed from one or more transcript files.
//
// The four token categories plus Unclassified are MUTUALLY EXCLUSIVE by
// construction, so Total() is a real total rather than a sum of overlapping
// buckets. Each reader is responsible for producing an exclusive breakdown; a
// provider aggregate that can't be broken down (legacy Codex) lands in
// Unclassified rather than being misattributed to a semantic category.
//
// See docs/design/2026-07-13-per-session-token-accounting.md.
type Usage struct {
	Input         int64 // fresh, uncached input tokens
	Output        int64 // completion tokens
	CacheCreation int64 // cache-write tokens (Claude only; Codex has no such concept)
	CacheRead     int64 // cache-hit input tokens
	Unclassified  int64 // provider aggregate we can't break down (legacy Codex)

	// Found reports whether at least one valid usage record was observed. A
	// transcript with no usage records yields Found=false — distinct from a
	// genuine zero — so callers never present a confident zero for a file they
	// couldn't read usage from.
	Found bool
	// Dropped counts records that were skipped or conflicted (format drift,
	// duplicate usage with differing values, un-deduplicatable records). A
	// nonzero value flags the total as approximate.
	Dropped int
}

// Total is the grand total across the exclusive categories.
func (u Usage) Total() int64 {
	return u.Input + u.Output + u.CacheCreation + u.CacheRead + u.Unclassified
}

// add merges another Usage into u, summing counters and OR-ing Found. Used to
// combine multiple source files for one session.
func (u *Usage) add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheCreation += o.CacheCreation
	u.CacheRead += o.CacheRead
	u.Unclassified += o.Unclassified
	u.Dropped += o.Dropped

	if o.Found {
		u.Found = true
	}
}

// Source is one on-disk transcript file contributing to a session's usage,
// with the stat fields the daemon fingerprints to decide whether a re-parse is
// needed (unchanged size+mtime ⇒ reuse the cached Usage without reading).
type Source struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// usageReader parses token usage from a single transcript file. Separate from
// the migration `reader` interface because usage counting sums EVERY
// usage-bearing record (including sidechains/subagent turns and abandoned
// branches — those tokens were spent), whereas migration walks only the active
// leaf chain.
type usageReader interface {
	usage(path string) (Usage, error)
}

func usageReaderFor(agent string) (usageReader, error) {
	switch agent {
	case AgentClaude:
		return claudeReader{}, nil
	case AgentCodex:
		return codexReader{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAgent, agent)
	}
}

// Locate returns every readable transcript file for the current agent of a
// session, with stat metadata for fingerprinting. It resolves without parsing
// so the caller can skip a re-read when nothing changed.
//
// Claude resolves to a single file (its transcript is one JSONL keyed by the
// agent session id). Codex resolves to the single located rollout in v1;
// summing across a resumed session's multiple rollouts is deferred until the
// resume append-vs-fork behaviour is confirmed with a real fixture (#644), to
// avoid over-counting unrelated same-cwd sessions in the meantime. The []Source
// return keeps that a drop-in extension.
func Locate(agent, agentSessionID, worktreePath string) ([]Source, error) {
	path, err := locate(agent, agentSessionID, worktreePath)
	if err != nil {
		return nil, err
	}

	src, err := statSource(path)
	if err != nil {
		return nil, err
	}

	return []Source{src}, nil
}

// UsageFrom parses the given sources and returns their combined usage. Unlike
// Read it never returns ErrNoTurns: a transcript with no usage records yields a
// zero Usage with Found=false, not an error.
func UsageFrom(agent string, sources []Source) (Usage, error) {
	r, err := usageReaderFor(agent)
	if err != nil {
		return Usage{}, err
	}

	var total Usage

	for _, s := range sources {
		u, err := r.usage(s.Path)
		if err != nil {
			return Usage{}, fmt.Errorf("read %s usage %s: %w", agent, s.Path, err)
		}

		total.add(u)
	}

	return total, nil
}

// statSource stats a transcript file into a Source.
func statSource(path string) (Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Source{}, err
	}

	return Source{Path: path, Size: fi.Size(), ModTime: fi.ModTime()}, nil
}
