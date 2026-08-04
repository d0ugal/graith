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
