package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestRunRendersSnapshotArtifacts(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "fixture.json")
	outDir := filepath.Join(dir, "out")

	fixture := `{
  "now": "2026-08-04T12:00:00Z",
  "profile": "preview",
  "current_session_id": "braw",
  "shortcut_keys": "123",
  "preview": "go test ./internal/client\nok github.com/d0ugal/graith/internal/client 0.123s",
  "sessions": [
    {
      "id": "braw",
      "name": "braw",
      "repo_name": "graith",
      "status": "running",
      "agent": "codex",
      "created_at": "2026-08-04T11:00:00Z",
      "last_output_at": "2026-08-04T11:59:00Z",
      "summary_text": "Render canny deterministic previews"
    }
  ],
  "scenes": [
    {
      "name": "canny",
      "view": "all",
      "session_id": "braw"
    }
  ]
}`
	if err := os.WriteFile(fixturePath, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer

	code := run([]string{
		"-fixture", fixturePath,
		"-out", outDir,
		"-sizes", "small:80x24,wide:160x30",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}

	for _, name := range []string{
		"canny-small.ansi",
		"canny-small.txt",
		"canny-wide.ansi",
		"canny-wide.txt",
		"pages.json",
		"viewports.json",
	} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}

	ansiData, err := os.ReadFile(filepath.Join(outDir, "canny-small.ansi"))
	if err != nil {
		t.Fatalf("read ansi: %v", err)
	}

	if !strings.Contains(string(ansiData), "\x1b[") {
		t.Fatalf("ANSI snapshot has no terminal styling:\n%s", ansiData)
	}

	txtData, err := os.ReadFile(filepath.Join(outDir, "canny-small.txt"))
	if err != nil {
		t.Fatalf("read text: %v", err)
	}

	for _, want := range []string{"Session Navigator", "braw"} {
		if !strings.Contains(string(txtData), want) {
			t.Fatalf("text snapshot missing %q:\n%s", want, txtData)
		}
	}

	pagesData, err := os.ReadFile(filepath.Join(outDir, "pages.json"))
	if err != nil {
		t.Fatalf("read pages: %v", err)
	}

	for _, want := range []string{`"name": "canny"`, `"small"`, `"wide"`} {
		if !strings.Contains(string(pagesData), want) {
			t.Fatalf("pages metadata missing %q:\n%s", want, pagesData)
		}
	}

	if !strings.Contains(stdout.String(), "rendered 1 page(s) across 2 terminal size(s)") {
		t.Fatalf("unexpected stdout:\n%s", stdout.String())
	}
}

func TestRunUsesDocsSuiteDefaults(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer

	code := run([]string{
		"-suite", "docs",
		"-fixture", filepath.Join("testdata", "session_navigator_docs_fixture.json"),
		"-out", outDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "rendered 5 page(s) across 1 terminal size(s)") {
		t.Fatalf("unexpected stdout:\n%s", stdout.String())
	}

	pages, viewports := readGeneratedMetadata(t, outDir)

	pageNames := make(map[string]bool, len(pages))
	for _, page := range pages {
		pageNames[page.Name] = true
		if len(page.Viewports) != 1 || page.Viewports[0] != "docs" {
			t.Fatalf("page %s viewports = %v, want [docs]", page.Name, page.Viewports)
		}
	}

	for _, want := range []string{
		"session-navigator-list",
		"session-navigator-labels",
		"session-navigator-repo",
		"session-navigator-jailed-warning",
		"session-navigator-orchestrator-attention",
	} {
		if !pageNames[want] {
			t.Fatalf("docs pages missing %q: %+v", want, pages)
		}
	}

	if len(viewports) != 1 || viewports[0].Label != "docs" || viewports[0].Width != 120 || viewports[0].Height != 30 {
		t.Fatalf("docs viewports = %+v, want one docs 120x30 viewport", viewports)
	}

	textData, err := os.ReadFile(filepath.Join(outDir, "session-navigator-list-docs.txt"))
	if err != nil {
		t.Fatalf("read docs text: %v", err)
	}

	text := string(textData)
	for _, want := range []string{"Session Navigator", "session-navigator-doc-screenshots", "orchestrator", "✉ 2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("docs text missing %q:\n%s", want, text)
		}
	}

	if got := len(strings.Split(text, "\n")); got != 30 {
		t.Fatalf("docs text line count = %d, want 30:\n%s", got, text)
	}
}

func TestSuiteDefaults(t *testing.T) {
	tests := map[string]suiteDefaults{
		"preview": {
			FixturePath: "internal/client/testdata/session_navigator_screenshot_fixture.json",
			OutDir:      "shots/session-navigator/ansi",
			Sizes:       "small:80x24,normal:120x30,wide:240x40",
		},
		"docs": {
			FixturePath: "cmd/sessionnavshots/testdata/session_navigator_docs_fixture.json",
			OutDir:      "shots/session-navigator/docs/ansi",
			Sizes:       "docs:120x30",
		},
	}

	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := defaultsForSuite(name)
			if err != nil {
				t.Fatal(err)
			}

			if got != want {
				t.Fatalf("defaultsForSuite(%q) = %+v, want %+v", name, got, want)
			}
		})
	}
}

func TestDocsFixtureDefinesAttentionScenes(t *testing.T) {
	fx, err := loadFixture(filepath.Join("testdata", "session_navigator_docs_fixture.json"))
	if err != nil {
		t.Fatalf("load docs fixture: %v", err)
	}

	scenes := make(map[string]snapshotScene, len(fx.Scenes))
	for _, scene := range fx.Scenes {
		scenes[scene.Name] = scene
	}

	tests := map[string]func(protocol.FleetSummary) bool{
		"session-navigator-jailed-warning": func(fleet protocol.FleetSummary) bool {
			return fleet.JailedComments == 2 && fleet.JailedNewestAuthor == "scunner" && fleet.JailedNewestPR == 43
		},
		"session-navigator-orchestrator-attention": func(fleet protocol.FleetSummary) bool {
			return fleet.OrchestratorAttention == "Review release checklist"
		},
	}

	for sceneName, validate := range tests {
		scene, ok := scenes[sceneName]
		if !ok {
			t.Fatalf("docs fixture missing %s scene", sceneName)
		}

		if scene.StatusBar == nil {
			t.Fatalf("%s scene has no status bar override", sceneName)
		}

		statusBar, err := fx.statusBarOptions(*scene.StatusBar)
		if err != nil {
			t.Fatalf("%s status bar options: %v", sceneName, err)
		}

		if !validate(statusBar.Fleet) {
			t.Fatalf("%s status bar fleet missing attention signal: %+v", sceneName, statusBar.Fleet)
		}
	}
}

func TestDocsWarningScenesRenderStatusBarWarnings(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	fixturePath := filepath.Join("testdata", "session_navigator_docs_fixture.json")

	if _, _, err := renderSnapshots(fixturePath, outDir, filepath.Join(outDir, "pages.json"), filepath.Join(outDir, "viewports.json"), defaultDocsSizes); err != nil {
		t.Fatalf("render docs fixture: %v", err)
	}

	tests := map[string][]string{
		"session-navigator-jailed-warning-docs.txt":         {"⚠ jail 2", "@scunner #43"},
		"session-navigator-orchestrator-attention-docs.txt": {"‼ orch", "Review release checklist"},
	}

	for filename, wants := range tests {
		t.Run(filename, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(outDir, filename))
			if err != nil {
				t.Fatalf("read warning scene: %v", err)
			}

			status := lastLine(string(data))
			for _, want := range wants {
				if !strings.Contains(status, want) {
					t.Fatalf("%s status bar missing %q:\n%s", filename, want, status)
				}
			}
		})
	}
}

func TestRunRendersStatusBarChrome(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "fixture.json")
	outDir := filepath.Join(dir, "out")

	fixture := `{
  "now": "2026-08-04T12:00:00Z",
  "profile": "preview",
  "current_session_id": "braw",
  "shortcut_keys": "123",
  "sessions": [
    {
      "id": "braw",
      "name": "braw",
      "repo_name": "graith",
      "status": "running",
      "agent_status": "active",
      "agent": "codex",
      "branch": "d0ugal/graith/status-bar-shots",
      "created_at": "2026-08-04T11:00:00Z",
      "last_output_at": "2026-08-04T11:59:00Z",
      "summary_text": "Render canny deterministic previews"
    }
  ],
  "status_bar": {
    "session_id": "braw",
    "unread_count": 2,
    "position": "bottom",
    "fleet": {
      "total": 3,
      "active": 2,
      "ready": 1,
      "jailed_comments": 1,
      "jailed_newest_author": "scunner",
      "jailed_newest_pr": 2086,
      "orchestrator_attention": "Need release decision"
    }
  },
  "scenes": [
    {
      "name": "canny",
      "view": "all",
      "session_id": "braw"
    }
  ]
}`
	if err := os.WriteFile(fixturePath, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer

	code := run([]string{
		"-fixture", fixturePath,
		"-out", outDir,
		"-sizes", "normal:100x24",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}

	data, err := os.ReadFile(filepath.Join(outDir, "canny-normal.txt"))
	if err != nil {
		t.Fatalf("read text: %v", err)
	}

	text := string(data)
	for _, want := range []string{"Session Navigator", "braw", "codex", "status-bar-shots", "active", "✉ 2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status bar snapshot missing %q:\n%s", want, text)
		}
	}

	lines := strings.Split(text, "\n")
	if len(lines) != 24 {
		t.Fatalf("text line count = %d, want 24:\n%s", len(lines), text)
	}

	for i, line := range lines {
		if width := ansi.StringWidth(line); width > 100 {
			t.Fatalf("line %d width = %d, want <= 100:\n%s", i+1, width, line)
		}
	}
}

func TestRenderDefaultFixtureIsStableAcrossProcessHome(t *testing.T) {
	originalHome := os.Getenv("HOME")

	t.Cleanup(func() {
		if originalHome == "" {
			_ = os.Unsetenv("HOME")
		} else {
			_ = os.Setenv("HOME", originalHome)
		}
	})

	outputs := make([]string, 0, 2)

	for _, home := range []string{"/home/runner", "/var/empty"} {
		if err := os.Setenv("HOME", home); err != nil {
			t.Fatalf("set HOME: %v", err)
		}

		outDir := filepath.Join(t.TempDir(), "out")
		fixturePath := filepath.Join("..", "..", defaultPreviewFixturePath)

		pages, viewports, err := renderSnapshots(fixturePath, outDir, filepath.Join(outDir, "pages.json"), filepath.Join(outDir, "viewports.json"), defaultPreviewSizes)
		if err != nil {
			t.Fatalf("render default fixture with HOME=%s: %v", home, err)
		}

		if len(pages) != 4 {
			t.Fatalf("pages = %d, want 4", len(pages))
		}

		pageNames := make(map[string]bool, len(pages))
		for _, page := range pages {
			pageNames[page.Name] = true
		}

		for _, want := range []string{
			"session-navigator-all",
			"session-navigator-repo",
			"session-navigator-labels",
			"session-navigator-deleted",
		} {
			if !pageNames[want] {
				t.Fatalf("pages missing %q: %+v", want, pages)
			}
		}

		if len(viewports) != 3 {
			t.Fatalf("viewports = %d, want 3", len(viewports))
		}

		normalData, err := os.ReadFile(filepath.Join(outDir, "session-navigator-all-normal.txt"))
		if err != nil {
			t.Fatalf("read normal text: %v", err)
		}

		wideData, err := os.ReadFile(filepath.Join(outDir, "session-navigator-all-wide.txt"))
		if err != nil {
			t.Fatalf("read wide text: %v", err)
		}

		repoData, err := os.ReadFile(filepath.Join(outDir, "session-navigator-repo-normal.txt"))
		if err != nil {
			t.Fatalf("read repo text: %v", err)
		}

		repoWideData, err := os.ReadFile(filepath.Join(outDir, "session-navigator-repo-wide.txt"))
		if err != nil {
			t.Fatalf("read repo wide text: %v", err)
		}

		labelsData, err := os.ReadFile(filepath.Join(outDir, "session-navigator-labels-normal.txt"))
		if err != nil {
			t.Fatalf("read labels text: %v", err)
		}

		deletedData, err := os.ReadFile(filepath.Join(outDir, "session-navigator-deleted-normal.txt"))
		if err != nil {
			t.Fatalf("read deleted text: %v", err)
		}

		normalText := string(normalData)
		if !strings.Contains(normalText, "~/src/graith") {
			t.Fatalf("normal text missing shortened home path:\n%s", normalText)
		}

		normalStatus := lastLine(normalText)
		for _, want := range []string{"orchestrator", "graith", "✉ 3"} {
			if !strings.Contains(normalStatus, want) {
				t.Fatalf("normal status bar missing %q:\n%s", want, normalStatus)
			}
		}

		wideText := string(wideData)

		wideStatus := lastLine(wideText)
		for _, want := range []string{"3 active", "1 ready", "1 error", "2 stopped"} {
			if !strings.Contains(wideStatus, want) {
				t.Fatalf("wide status bar missing %q:\n%s", want, wideStatus)
			}
		}

		for _, want := range []string{"#1870", "review-needed"} {
			if !strings.Contains(wideText, want) {
				t.Fatalf("wide text missing %q:\n%s", want, wideText)
			}
		}

		repoWideStatus := lastLine(string(repoWideData))
		for _, want := range []string{"session-navigator-preview", "PR#1870", "↑2"} {
			if !strings.Contains(repoWideStatus, want) {
				t.Fatalf("repo wide status bar missing %q:\n%s", want, repoWideStatus)
			}
		}

		repoText := string(repoData)
		for _, want := range []string{"Repo", "graith (", "session-navigator-screenshots"} {
			if !strings.Contains(repoText, want) {
				t.Fatalf("repo text missing %q:\n%s", want, repoText)
			}
		}

		labelsText := string(labelsData)
		for _, want := range []string{"Labels", "visual-regression", "session-navigator-screenshots"} {
			if !strings.Contains(labelsText, want) {
				t.Fatalf("labels text missing %q:\n%s", want, labelsText)
			}
		}

		deletedText := string(deletedData)
		for _, want := range []string{"Deleted", "deleted-stopped-session", "enter restore"} {
			if !strings.Contains(deletedText, want) {
				t.Fatalf("deleted text missing %q:\n%s", want, deletedText)
			}
		}

		outputs = append(outputs, normalText+"\n---\n"+wideText)
	}

	if outputs[0] != outputs[1] {
		t.Fatal("default fixture render changed with process HOME")
	}
}

func lastLine(text string) string {
	lines := strings.Split(text, "\n")
	return lines[len(lines)-1]
}

func TestDefaultFixtureSnapshotsFitTerminalGeometry(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	fixturePath := filepath.Join("..", "..", defaultPreviewFixturePath)

	pages, viewports, err := renderSnapshots(fixturePath, outDir, filepath.Join(outDir, "pages.json"), filepath.Join(outDir, "viewports.json"), defaultPreviewSizes)
	if err != nil {
		t.Fatalf("render default fixture: %v", err)
	}

	sizes := make(map[string]terminalSize, len(viewports))
	for _, viewport := range viewports {
		sizes[viewport.Label] = viewport
	}

	for _, page := range pages {
		for _, viewport := range page.Viewports {
			size, ok := sizes[viewport]
			if !ok {
				t.Fatalf("page %s references unknown viewport %q", page.Name, viewport)
			}

			path := filepath.Join(outDir, page.Name+"-"+viewport+".ansi")

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			rendered := string(data)
			if strings.HasSuffix(rendered, "\n") {
				t.Fatalf("%s has trailing newline", path)
			}

			lines := strings.Split(rendered, "\n")
			if len(lines) != size.Height {
				t.Fatalf("%s line count = %d, want %d", path, len(lines), size.Height)
			}

			if !strings.Contains(rendered, "╰") {
				t.Fatalf("%s missing bottom panel border; snapshot is likely clipped:\n%s", path, rendered)
			}

			if !strings.Contains(rendered, "q quit") {
				t.Fatalf("%s missing compact help footer; snapshot is likely clipped:\n%s", path, rendered)
			}

			for i, line := range lines {
				if width := ansi.StringWidth(line); width > size.Width {
					t.Fatalf("%s line %d width = %d, want <= %d", path, i+1, width, size.Width)
				}
			}
		}
	}
}

func readGeneratedMetadata(t *testing.T, outDir string) ([]pageMetadata, []terminalSize) {
	t.Helper()

	pagesData, err := os.ReadFile(filepath.Join(outDir, "pages.json"))
	if err != nil {
		t.Fatalf("read pages metadata: %v", err)
	}

	var pages []pageMetadata
	if err := json.Unmarshal(pagesData, &pages); err != nil {
		t.Fatalf("decode pages metadata: %v", err)
	}

	viewportsData, err := os.ReadFile(filepath.Join(outDir, "viewports.json"))
	if err != nil {
		t.Fatalf("read viewport metadata: %v", err)
	}

	var viewports []terminalSize
	if err := json.Unmarshal(viewportsData, &viewports); err != nil {
		t.Fatalf("decode viewport metadata: %v", err)
	}

	return pages, viewports
}

func TestRunRejectsInvalidTerminalSize(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "fixture.json")

	if err := os.WriteFile(fixturePath, []byte(`{"now":"2026-08-04T12:00:00Z"}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer

	code := run([]string{
		"-fixture", fixturePath,
		"-out", filepath.Join(dir, "out"),
		"-sizes", "bad",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("run exit = 0, want failure")
	}

	if !strings.Contains(stderr.String(), "invalid terminal size") {
		t.Fatalf("stderr missing validation message:\n%s", stderr.String())
	}
}

func TestRunRejectsDuplicateSceneName(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "fixture.json")

	fixture := `{
  "now": "2026-08-04T12:00:00Z",
  "sessions": [{"id":"braw","name":"braw","created_at":"2026-08-04T11:00:00Z"}],
  "scenes": [
    {"name":"canny","view":"all","session_id":"braw"},
    {"name":"canny","view":"all","session_id":"braw"}
  ]
}`
	if err := os.WriteFile(fixturePath, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer

	code := run([]string{
		"-fixture", fixturePath,
		"-out", filepath.Join(dir, "out"),
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("run exit = 0, want failure")
	}

	if !strings.Contains(stderr.String(), "duplicate scene name") {
		t.Fatalf("stderr missing duplicate scene message:\n%s", stderr.String())
	}
}
