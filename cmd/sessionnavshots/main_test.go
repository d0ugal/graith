package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
		fixturePath := filepath.Join("..", "..", defaultFixturePath)

		pages, viewports, err := renderSnapshots(fixturePath, outDir, filepath.Join(outDir, "pages.json"), filepath.Join(outDir, "viewports.json"), defaultSizes)
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

		wideText := string(wideData)
		for _, want := range []string{"#1870", "review-needed"} {
			if !strings.Contains(wideText, want) {
				t.Fatalf("wide text missing %q:\n%s", want, wideText)
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

func TestDefaultFixtureSnapshotsFitTerminalGeometry(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	fixturePath := filepath.Join("..", "..", defaultFixturePath)

	pages, viewports, err := renderSnapshots(fixturePath, outDir, filepath.Join(outDir, "pages.json"), filepath.Join(outDir, "viewports.json"), defaultSizes)
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
