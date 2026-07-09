package client

import "image/color"

// StatusColor returns the palette color for a session lifecycle status,
// matching the colors used by the overlay's status column: running is green,
// errored is red, and everything else (stopped, unknown) is dimmed.
//
// It is exported so other renderers (e.g. `gr list`) can share the overlay's
// palette instead of duplicating the hex values.
func StatusColor(status string) color.Color {
	switch status {
	case "running":
		return colorGreen
	case "errored":
		return colorRed
	default:
		return colorDim
	}
}

// AgentStatusColor returns the palette color for an agent activity status,
// mirroring the overlay's status-column mapping: approval is red, active/running
// is green, ready is blue, and anything else is dimmed.
func AgentStatusColor(agentStatus string) color.Color {
	switch agentStatus {
	case "approval":
		return colorRed
	case "active", "running":
		return colorGreen
	case "ready":
		return colorBlue
	default:
		return colorDim
	}
}
