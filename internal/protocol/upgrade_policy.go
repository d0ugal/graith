package protocol

import "time"

// UpgradeNegotiationTimeout covers the daemon's bounded pre-ack work: target
// probing, the 15-second lifecycle/mutation admission drain, the 5-second
// terminal-helper freeze, and bounded handoff preparation overhead.
const UpgradeNegotiationTimeout = 30 * time.Second

// UpgradeReadinessTimeout covers all post-ack work before the replacement can
// answer: background drains, PTY quiescence, exec, and the replacement
// daemon's 15-second adoption window, with margin for startup I/O.
const UpgradeReadinessTimeout = 60 * time.Second
