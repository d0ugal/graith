package client

import "time"

// Terminal / TUI presentation preferences resolved from the [terminal] config
// block (issue #1254). These are package vars rather than consts so
// ConfigurePresentation can install config-derived values at CLI startup, and so
// tests can override them. The initial values reproduce the behaviour that was
// hard-coded before the [terminal] config block existed.
//
// Only genuine user preferences live here. Layout invariants (column-width
// arithmetic, wrap widths, minimum name width, panel breakpoints) stay as
// documented constants next to the render logic they must match.
var (
	// refreshInterval is the cadence at which the session picker, dashboard,
	// message viewer, and an attached status bar re-poll the daemon for fresh
	// session state.
	refreshInterval = 2 * time.Second

	// fallbackCols / fallbackRows are the terminal geometry used only when the
	// real terminal size cannot be probed (e.g. stdout is piped, not a TTY).
	fallbackCols uint16 = 80
	fallbackRows uint16 = 24

	// summaryWidth caps the visible width of a `gr status` summary shown in the
	// picker before it is truncated with an ellipsis. It also bounds the
	// auto-sizing Summary column (see SessionColumns), so the two stay in step.
	summaryWidth = maxSummaryWidth
)

// PresentationPrefs carries the [terminal] presentation values resolved from
// config. It is defined here (not in internal/config) so config need not import
// client; the CLI maps config accessors into this struct and hands it to
// ConfigurePresentation, mirroring ConnectionTimeouts / ConfigureConnection.
type PresentationPrefs struct {
	RefreshInterval time.Duration
	DefaultCols     int
	DefaultRows     int
	SummaryWidth    int
}

// ConfigurePresentation installs the configured terminal presentation values
// into the package vars the TUI and handshake paths read. It is called once from
// the CLI's pre-run after config is loaded. A non-positive field is ignored so a
// partially populated struct can't zero a value; callers pass values already
// defaulted by the config accessors.
func ConfigurePresentation(p PresentationPrefs) {
	if p.RefreshInterval > 0 {
		refreshInterval = p.RefreshInterval
	}

	if p.DefaultCols > 0 {
		fallbackCols = uint16(p.DefaultCols) //nolint:gosec // G115: config accessor clamps to a small positive int
	}

	if p.DefaultRows > 0 {
		fallbackRows = uint16(p.DefaultRows) //nolint:gosec // G115: config accessor clamps to a small positive int
	}

	if p.SummaryWidth > 0 {
		summaryWidth = p.SummaryWidth
	}
}

// FallbackGeometry returns the configured fallback terminal geometry used when a
// real terminal size cannot be probed. Exposed so the cli package's remote
// attach path shares the same defaults as the local client.
func FallbackGeometry() (cols, rows uint16) {
	return fallbackCols, fallbackRows
}
