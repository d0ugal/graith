package client

import (
	"errors"
	"fmt"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

var snapshotClockMu sync.Mutex

// SessionNavigatorSnapshotOptions configures a non-interactive Session
// Navigator render for CI screenshots and snapshot tests.
type SessionNavigatorSnapshotOptions struct {
	Sessions         []protocol.SessionInfo
	DeletedSessions  []protocol.SessionInfo
	CurrentSessionID string
	Profile          string
	Collapsed        map[string]bool
	State            SessionNavigatorState
	RepoSuggestions  []RepoSuggestion
	ShortcutKeys     string
	Agents           []string
	DefaultAgent     string
	Keys             SessionNavigatorKeys
	Help             SessionNavigatorHelp
	SelectedDetail   *SelectedDetailConfig
	Preview          string
	Now              time.Time
	HomeDir          string
	Width            int
	Height           int
}

// SessionNavigatorStatusBarSnapshotOptions configures the Graith-owned
// terminal chrome rendered around a TUI snapshot.
type SessionNavigatorStatusBarSnapshotOptions struct {
	Session     protocol.SessionInfo
	Fleet       protocol.FleetSummary
	UnreadCount int
	ReadOnly    bool
	Position    string
}

// SessionNavigatorTerminalSnapshotOptions configures a full terminal render:
// the Session Navigator child frame plus the status bar/chrome row shown while
// attached.
type SessionNavigatorTerminalSnapshotOptions struct {
	Navigator SessionNavigatorSnapshotOptions
	StatusBar SessionNavigatorStatusBarSnapshotOptions
}

// RenderSessionNavigatorSnapshot renders the same Bubble Tea/Lip Gloss view as
// the live Session Navigator without starting an interactive terminal program.
func RenderSessionNavigatorSnapshot(opts SessionNavigatorSnapshotOptions) (string, error) {
	if opts.Width <= 0 {
		return "", errors.New("snapshot width must be positive")
	}

	if opts.Height <= 0 {
		return "", errors.New("snapshot height must be positive")
	}

	if !opts.Now.IsZero() || opts.HomeDir != "" {
		snapshotClockMu.Lock()
		defer snapshotClockMu.Unlock()
	}

	if !opts.Now.IsZero() {
		restore := installSnapshotClock(opts.Now)
		defer restore()
	}

	if opts.HomeDir != "" {
		restore := installSnapshotHomeDir(opts.HomeDir)
		defer restore()
	}

	m := newSessionNavigatorModel(sessionNavigatorModelOptions{
		Sessions:         opts.Sessions,
		DeletedSessions:  opts.DeletedSessions,
		CurrentSessionID: opts.CurrentSessionID,
		FetchPreview: func(string) string {
			return opts.Preview
		},
		Profile:         opts.Profile,
		Collapsed:       opts.Collapsed,
		State:           opts.State,
		RepoSuggestions: opts.RepoSuggestions,
		ShortcutKeys:    opts.ShortcutKeys,
		Agents:          opts.Agents,
		DefaultAgent:    opts.DefaultAgent,
		Keys:            opts.Keys,
		Help:            opts.Help,
		SelectedDetail:  opts.SelectedDetail,
	})
	m.previewContent = opts.Preview

	updated, _ := m.Update(tea.WindowSizeMsg{Width: opts.Width, Height: opts.Height})

	rendered, ok := updated.(*overlayModel)
	if !ok {
		return "", fmt.Errorf("snapshot renderer returned %T", updated)
	}

	return rendered.View().Content, nil
}

// RenderSessionNavigatorTerminalSnapshot renders the Session Navigator inside
// the Graith-owned terminal chrome used for attached sessions.
func RenderSessionNavigatorTerminalSnapshot(opts SessionNavigatorTerminalSnapshotOptions) (string, error) {
	position := opts.StatusBar.Position
	if position == "" {
		position = "bottom"
	}

	if position != "top" && position != "bottom" {
		return "", fmt.Errorf("status bar position must be top or bottom, got %q", position)
	}

	if opts.Navigator.Height <= 1 {
		return "", errors.New("terminal snapshot height must leave room for status bar")
	}

	navigator := opts.Navigator
	navigator.Height--

	rendered, err := RenderSessionNavigatorSnapshot(navigator)
	if err != nil {
		return "", err
	}

	info := newStatusBarInfo(opts.StatusBar.Session, opts.StatusBar.UnreadCount, opts.StatusBar.Fleet)
	statusLine := formatStatusLine(info, opts.Navigator.Width)

	if opts.StatusBar.ReadOnly {
		statusLine = formatReadOnlyLine(info, opts.Navigator.Width)
	}

	if position == "top" {
		return statusLine + "\n" + rendered, nil
	}

	return rendered + "\n" + statusLine, nil
}

func installSnapshotClock(now time.Time) func() {
	ensureOverlayProviders()

	previous := overlayNowValue.Load()
	overlayNowValue.Store(func() time.Time {
		return now
	})

	return func() {
		overlayNowValue.Store(previous)
	}
}

func installSnapshotHomeDir(homeDir string) func() {
	ensureOverlayProviders()

	previous := overlayUserHomeDirValue.Load()
	overlayUserHomeDirValue.Store(func() (string, error) {
		return homeDir, nil
	})

	return func() {
		overlayUserHomeDirValue.Store(previous)
	}
}
