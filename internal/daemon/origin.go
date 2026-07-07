package daemon

// TailnetIdentity is the resolved Tailscale identity behind a remote
// connection, obtained via the tailnet LocalClient's WhoIs at accept time.
// User and Node are stable Tailscale identifiers (not display names). For a
// tagged node WhoIs returns no user, so User is empty and Tags carries the
// node's tags.
type TailnetIdentity struct {
	User string
	Node string
	Tags []string
}

// ConnOrigin describes where a connection came from. It is threaded into
// HandleConnection so authorization can distinguish the local Unix socket (the
// 0700 trust boundary) from a remote tailnet connection, and recover the
// tailnet identity for the latter.
//
// The zero value (Remote: false, Identity: nil) is a local Unix-socket
// connection — the existing trust model — so existing callers that pass
// ConnOrigin{} keep today's behaviour.
type ConnOrigin struct {
	// Remote is false for the local Unix socket, true for a tailnet listener.
	Remote bool
	// Identity is the WhoIs result for remote connections; nil for local.
	Identity *TailnetIdentity
}
