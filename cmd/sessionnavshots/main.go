// Command sessionnavshots renders deterministic Session Navigator snapshots for
// PR screenshot previews and documentation.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
)

const (
	defaultSuite = "preview"

	defaultPreviewFixturePath = "internal/client/testdata/session_navigator_screenshot_fixture.json"
	defaultPreviewOutDir      = "shots/session-navigator/ansi"
	defaultPreviewSizes       = "small:80x24,normal:120x30,wide:240x40"

	defaultDocsFixturePath = "cmd/sessionnavshots/testdata/session_navigator_docs_fixture.json"
	defaultDocsOutDir      = "shots/session-navigator/docs/ansi"
	defaultDocsSizes       = "docs:120x30"
)

type fixture struct {
	Now              string                 `json:"now"`
	HomeDir          string                 `json:"home_dir"`
	Profile          string                 `json:"profile"`
	CurrentSessionID string                 `json:"current_session_id"`
	ShortcutKeys     string                 `json:"shortcut_keys"`
	Preview          string                 `json:"preview"`
	Collapsed        map[string]bool        `json:"collapsed"`
	Sessions         []protocol.SessionInfo `json:"sessions"`
	DeletedSessions  []protocol.SessionInfo `json:"deleted_sessions"`
	StatusBar        *statusBarFixture      `json:"status_bar"`
	Scenes           []snapshotScene        `json:"scenes"`
}

type snapshotScene struct {
	Name         string            `json:"name"`
	View         string            `json:"view"`
	SessionID    string            `json:"session_id"`
	LabelGroup   string            `json:"label_group"`
	HelpExpanded bool              `json:"help_expanded"`
	StatusBar    *statusBarFixture `json:"status_bar"`
}

type statusBarFixture struct {
	SessionID   string          `json:"session_id"`
	Fleet       json.RawMessage `json:"fleet"`
	UnreadCount int             `json:"unread_count"`
	ReadOnly    bool            `json:"read_only"`
	Position    string          `json:"position"`
}

type terminalSize struct {
	Label  string `json:"label"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type pageMetadata struct {
	Name      string   `json:"name"`
	HasBase   bool     `json:"hasBase"`
	Deleted   bool     `json:"deleted"`
	Viewports []string `json:"viewports,omitempty"`
}

type suiteDefaults struct {
	FixturePath string
	OutDir      string
	Sizes       string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sessionnavshots", flag.ContinueOnError)
	flags.SetOutput(stderr)

	suiteName := flags.String("suite", defaultSuite, "snapshot suite to render: preview or docs")
	fixturePath := flags.String("fixture", "", "JSON fixture with fake Session Navigator data (defaults by suite)")
	outDir := flags.String("out", "", "directory for generated ANSI and text snapshots (defaults by suite)")
	pagesPath := flags.String("pages", "", "path for generated pages metadata JSON")
	viewportsPath := flags.String("viewports", "", "path for generated viewport metadata JSON")
	sizesValue := flags.String("sizes", "", "comma-delimited terminal sizes as label:COLSxROWS (defaults by suite)")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "sessionnavshots: unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	defaults, err := defaultsForSuite(*suiteName)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sessionnavshots: %v\n", err)
		return 2
	}

	if *fixturePath == "" {
		*fixturePath = defaults.FixturePath
	}

	if *outDir == "" {
		*outDir = defaults.OutDir
	}

	if *sizesValue == "" {
		*sizesValue = defaults.Sizes
	}

	if *pagesPath == "" {
		*pagesPath = filepath.Join(*outDir, "pages.json")
	}

	if *viewportsPath == "" {
		*viewportsPath = filepath.Join(*outDir, "viewports.json")
	}

	pages, viewports, err := renderSnapshots(*fixturePath, *outDir, *pagesPath, *viewportsPath, *sizesValue)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sessionnavshots: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "sessionnavshots: rendered %d page(s) across %d terminal size(s)\n", len(pages), len(viewports))

	return 0
}

func defaultsForSuite(name string) (suiteDefaults, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "preview", "pr-preview", "session-navigator-preview":
		return suiteDefaults{
			FixturePath: defaultPreviewFixturePath,
			OutDir:      defaultPreviewOutDir,
			Sizes:       defaultPreviewSizes,
		}, nil
	case "docs", "documentation":
		return suiteDefaults{
			FixturePath: defaultDocsFixturePath,
			OutDir:      defaultDocsOutDir,
			Sizes:       defaultDocsSizes,
		}, nil
	default:
		return suiteDefaults{}, fmt.Errorf("unknown suite %q", name)
	}
}

func renderSnapshots(fixturePath, outDir, pagesPath, viewportsPath, sizesValue string) ([]pageMetadata, []terminalSize, error) {
	fx, err := loadFixture(fixturePath)
	if err != nil {
		return nil, nil, err
	}

	now, err := parseFixtureTime(fx.Now)
	if err != nil {
		return nil, nil, err
	}

	sizes, err := parseTerminalSizes(sizesValue)
	if err != nil {
		return nil, nil, err
	}

	scenes := fx.Scenes
	if len(scenes) == 0 {
		scenes = []snapshotScene{{Name: "session-navigator", View: "all", SessionID: fx.CurrentSessionID}}
	}

	//nolint:gosec // G301: generated preview artifacts are intentionally world-readable.
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, nil, err
	}

	pages := make([]pageMetadata, 0, len(scenes))
	viewports := viewportLabels(sizes)
	seenScenes := map[string]bool{}

	for _, scene := range scenes {
		if !validArtifactName(scene.Name) {
			return nil, nil, fmt.Errorf("invalid scene name %q", scene.Name)
		}

		if seenScenes[scene.Name] {
			return nil, nil, fmt.Errorf("duplicate scene name %q", scene.Name)
		}

		seenScenes[scene.Name] = true

		state, err := scene.state()
		if err != nil {
			return nil, nil, err
		}

		help := client.DefaultSessionNavigatorHelp()
		help.ExpandedByDefault = scene.HelpExpanded

		statusBar := scene.StatusBar
		if statusBar == nil {
			statusBar = fx.StatusBar
		}

		for _, size := range sizes {
			navigatorOptions := client.SessionNavigatorSnapshotOptions{
				Sessions:         fx.Sessions,
				DeletedSessions:  fx.DeletedSessions,
				CurrentSessionID: fx.CurrentSessionID,
				Profile:          fx.Profile,
				Collapsed:        fx.Collapsed,
				State:            state,
				ShortcutKeys:     fx.ShortcutKeys,
				Help:             help,
				Preview:          fx.Preview,
				Now:              now,
				HomeDir:          fx.HomeDir,
				Width:            size.Width,
				Height:           size.Height,
			}

			rendered, err := renderSceneSnapshot(fx, navigatorOptions, statusBar)
			if err != nil {
				return nil, nil, fmt.Errorf("%s/%s: %w", scene.Name, size.Label, err)
			}

			stem := scene.Name + "-" + size.Label
			if err := writeSnapshotFiles(outDir, stem, rendered); err != nil {
				return nil, nil, err
			}
		}

		pages = append(pages, pageMetadata{
			Name:      scene.Name,
			Viewports: viewports,
		})
	}

	if err := writeJSON(pagesPath, pages); err != nil {
		return nil, nil, err
	}

	if err := writeJSON(viewportsPath, sizes); err != nil {
		return nil, nil, err
	}

	return pages, sizes, nil
}

func renderSceneSnapshot(fx fixture, navigatorOptions client.SessionNavigatorSnapshotOptions, statusBar *statusBarFixture) (string, error) {
	if statusBar == nil {
		return client.RenderSessionNavigatorSnapshot(navigatorOptions)
	}

	statusBarOptions, err := fx.statusBarOptions(*statusBar)
	if err != nil {
		return "", err
	}

	return client.RenderSessionNavigatorTerminalSnapshot(client.SessionNavigatorTerminalSnapshotOptions{
		Navigator: navigatorOptions,
		StatusBar: statusBarOptions,
	})
}

func (fx fixture) statusBarOptions(statusBar statusBarFixture) (client.SessionNavigatorStatusBarSnapshotOptions, error) {
	sessionID := statusBar.SessionID
	if sessionID == "" {
		sessionID = fx.CurrentSessionID
	}

	session, ok := fx.sessionByID(sessionID)
	if !ok {
		return client.SessionNavigatorStatusBarSnapshotOptions{}, fmt.Errorf("status bar session %q not found in fixture sessions", sessionID)
	}

	fleet := protocol.FleetSummary{}
	if len(statusBar.Fleet) > 0 {
		if err := json.Unmarshal(statusBar.Fleet, &fleet); err != nil {
			return client.SessionNavigatorStatusBarSnapshotOptions{}, fmt.Errorf("decode status bar fleet: %w", err)
		}
	}

	return client.SessionNavigatorStatusBarSnapshotOptions{
		Session:     session,
		Fleet:       fleet,
		UnreadCount: statusBar.UnreadCount,
		ReadOnly:    statusBar.ReadOnly,
		Position:    statusBar.Position,
	}, nil
}

func (fx fixture) sessionByID(id string) (protocol.SessionInfo, bool) {
	for _, session := range fx.Sessions {
		if session.ID == id {
			return session, true
		}
	}

	for _, session := range fx.DeletedSessions {
		if session.ID == id {
			return session, true
		}
	}

	return protocol.SessionInfo{}, false
}

func loadFixture(path string) (fixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fixture{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var fx fixture
	if err := decoder.Decode(&fx); err != nil {
		return fixture{}, err
	}

	return fx, nil
}

func parseFixtureTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, errors.New("fixture now is required")
	}

	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse fixture now: %w", err)
	}

	return t, nil
}

func parseTerminalSizes(value string) ([]terminalSize, error) {
	parts := strings.Split(value, ",")
	sizes := make([]terminalSize, 0, len(parts))
	seen := make(map[string]bool, len(parts))

	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		label, dims, ok := strings.Cut(raw, ":")
		if !ok {
			return nil, fmt.Errorf("invalid terminal size %q: want label:COLSxROWS", raw)
		}

		label = strings.TrimSpace(label)
		if !validArtifactName(label) {
			return nil, fmt.Errorf("invalid terminal size label %q", label)
		}

		if seen[label] {
			return nil, fmt.Errorf("duplicate terminal size label %q", label)
		}

		widthValue, heightValue, ok := strings.Cut(strings.ToLower(strings.TrimSpace(dims)), "x")
		if !ok {
			return nil, fmt.Errorf("invalid terminal size %q: want label:COLSxROWS", raw)
		}

		width, err := strconv.Atoi(widthValue)
		if err != nil || width <= 0 {
			return nil, fmt.Errorf("invalid width in terminal size %q", raw)
		}

		height, err := strconv.Atoi(heightValue)
		if err != nil || height <= 0 {
			return nil, fmt.Errorf("invalid height in terminal size %q", raw)
		}

		sizes = append(sizes, terminalSize{Label: label, Width: width, Height: height})
		seen[label] = true
	}

	if len(sizes) == 0 {
		return nil, errors.New("at least one terminal size is required")
	}

	return sizes, nil
}

func (scene snapshotScene) state() (client.SessionNavigatorState, error) {
	view, err := parseView(scene.View)
	if err != nil {
		return client.SessionNavigatorState{}, err
	}

	return client.SessionNavigatorState{
		View:       view,
		SessionID:  scene.SessionID,
		LabelGroup: scene.LabelGroup,
	}, nil
}

func parseView(value string) (client.SessionNavigatorView, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return client.SessionNavigatorViewAll, nil
	case "repo", "repository":
		return client.SessionNavigatorViewRepo, nil
	case "starred":
		return client.SessionNavigatorViewStarred, nil
	case "labels":
		return client.SessionNavigatorViewLabels, nil
	case "scenarios", "scenario":
		return client.SessionNavigatorViewScenario, nil
	case "deleted":
		return client.SessionNavigatorViewDeleted, nil
	default:
		return client.SessionNavigatorViewAll, fmt.Errorf("unknown view %q", value)
	}
}

func viewportLabels(sizes []terminalSize) []string {
	labels := make([]string, 0, len(sizes))
	for _, size := range sizes {
		labels = append(labels, size.Label)
	}

	return labels
}

func writeSnapshotFiles(outDir, stem, rendered string) error {
	ansiPath := filepath.Join(outDir, stem+".ansi")
	txtPath := filepath.Join(outDir, stem+".txt")

	//nolint:gosec // G306: generated preview artifacts are intentionally world-readable.
	if err := os.WriteFile(ansiPath, []byte(rendered), 0o644); err != nil {
		return err
	}

	//nolint:gosec // G306: generated preview artifacts are intentionally world-readable.
	return os.WriteFile(txtPath, []byte(ansi.Strip(rendered)), 0o644)
}

func writeJSON(path string, value any) error {
	//nolint:gosec // G301: workflow metadata should be readable.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')

	//nolint:gosec // G306: generated preview metadata is intentionally world-readable.
	return os.WriteFile(path, data, 0o644)
}

func validArtifactName(name string) bool {
	if name == "" || strings.Contains(name, "..") {
		return false
	}

	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}

		return false
	}

	return true
}
