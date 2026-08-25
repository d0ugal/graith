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
// mirroring the overlay's status-column mapping.
func AgentStatusColor(agentStatus string) color.Color {
	switch agentStatus {
	case "active", "running":
		return colorGreen
	case "ready":
		return colorBlue
	default:
		return colorDim
	}
}

// DependencyStateColor returns the shared palette colour for a dependency
// provider's reported state.
func DependencyStateColor(state string) color.Color {
	switch state {
	case "operational":
		return colorGreen
	case "degraded":
		return colorYellow
	case "down":
		return colorRed
	default:
		return colorDim
	}
}

// DependencySourceHealthColor returns the shared palette colour for whether a
// dependency status source can currently be trusted.
func DependencySourceHealthColor(health string) color.Color {
	switch health {
	case "fresh":
		return colorGreen
	case "stale":
		return colorYellow
	case "failed":
		return colorRed
	default:
		return colorDim
	}
}
