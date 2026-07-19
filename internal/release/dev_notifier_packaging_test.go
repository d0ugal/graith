package release

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

type devReleaseConfig struct {
	Before struct {
		Hooks []string `yaml:"hooks"`
	} `yaml:"before"`
	Builds []struct {
		ID     string   `yaml:"id"`
		Binary string   `yaml:"binary"`
		Goos   []string `yaml:"goos"`
		Goarch []string `yaml:"goarch"`
	} `yaml:"builds"`
	Archives []struct {
		ID    string        `yaml:"id"`
		IDs   []string      `yaml:"ids"`
		Files []archiveFile `yaml:"files"`
	} `yaml:"archives"`
}

func releaseRootPath(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

func loadDevReleaseConfig(t *testing.T) devReleaseConfig {
	t.Helper()

	data, err := os.ReadFile(releaseRootPath(".goreleaser-dev.yaml"))
	if err != nil {
		t.Fatalf("read .goreleaser-dev.yaml: %v", err)
	}

	var cfg devReleaseConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse .goreleaser-dev.yaml: %v", err)
	}

	return cfg
}

func TestDevGoReleaserBuildsNotifierOnlyIntoDarwinArchives(t *testing.T) {
	cfg := loadDevReleaseConfig(t)

	hasBuildHook := false
	hasFailClosedHook := false

	for _, hook := range cfg.Before.Hooks {
		hasBuildHook = hasBuildHook || strings.Contains(hook, "make notifier")
		hasFailClosedHook = hasFailClosedHook ||
			(strings.Contains(hook, `test "$(uname -s)" != Darwin`) &&
				strings.Contains(hook, "test -x macos/build/GraithNotifier.app/Contents/MacOS/graith-notifier"))
	}

	if !hasBuildHook {
		t.Fatal("dev GoReleaser config does not build GraithNotifier.app")
	}

	if !hasFailClosedHook {
		t.Fatal("dev GoReleaser config does not fail closed when the Darwin notifier executable is missing")
	}

	wantBuilds := map[string][]string{
		"gr-dev-linux":  {"linux"},
		"gr-dev-darwin": {"darwin"},
	}

	for id, wantOS := range wantBuilds {
		found := false

		for _, build := range cfg.Builds {
			if build.ID != id {
				continue
			}

			found = true

			if build.Binary != "gr-dev" {
				t.Errorf("build %q binary = %q, want gr-dev", id, build.Binary)
			}

			if !slices.Equal(build.Goos, wantOS) {
				t.Errorf("build %q goos = %v, want %v", id, build.Goos, wantOS)
			}

			if !slices.Equal(build.Goarch, []string{"amd64", "arm64"}) {
				t.Errorf("build %q goarch = %v, want amd64+arm64", id, build.Goarch)
			}
		}

		if !found {
			t.Errorf("dev GoReleaser config has no %q build", id)
		}
	}

	for _, tc := range []struct {
		archiveID string
		buildID   string
		wantApp   bool
	}{
		{archiveID: "linux", buildID: "gr-dev-linux", wantApp: false},
		{archiveID: "darwin", buildID: "gr-dev-darwin", wantApp: true},
	} {
		t.Run(tc.archiveID, func(t *testing.T) {
			for _, archive := range cfg.Archives {
				if archive.ID != tc.archiveID {
					continue
				}

				if !slices.Equal(archive.IDs, []string{tc.buildID}) {
					t.Fatalf("archive %q ids = %v, want [%s]", tc.archiveID, archive.IDs, tc.buildID)
				}

				hasApp := false

				for _, file := range archive.Files {
					if !strings.Contains(file.path(), "GraithNotifier.app") {
						continue
					}

					hasApp = true

					if file.Src != "macos/build/GraithNotifier.app/**/*" || file.Dst != "GraithNotifier.app/Contents" {
						t.Errorf("notifier archive mapping = src %q dst %q", file.Src, file.Dst)
					}
				}

				if hasApp != tc.wantApp {
					t.Errorf("archive %q contains notifier = %v, want %v", tc.archiveID, hasApp, tc.wantApp)
				}

				return
			}

			t.Fatalf("dev GoReleaser config has no %q archive", tc.archiveID)
		})
	}
}

type devReleaseWorkflow struct {
	Jobs map[string]struct {
		RunsOn string `yaml:"runs-on"`
		Steps  []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadDevReleaseWorkflow(t *testing.T) devReleaseWorkflow {
	t.Helper()

	data, err := os.ReadFile(releaseRootPath(".github", "workflows", "dev-release.yml"))
	if err != nil {
		t.Fatalf("read dev-release.yml: %v", err)
	}

	var workflow devReleaseWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse dev-release.yml: %v", err)
	}

	return workflow
}

func TestDevReleaseWorkflowValidatesNativeArchives(t *testing.T) {
	workflow := loadDevReleaseWorkflow(t)

	job, ok := workflow.Jobs["dev-release"]
	if !ok {
		t.Fatal("dev-release workflow has no dev-release job")
	}

	if job.RunsOn != "macos-latest" {
		t.Fatalf("dev-release runner = %q, want macos-latest for Swift notifier build", job.RunsOn)
	}

	var goreleaserArgs, verifyScript string

	for _, step := range job.Steps {
		if strings.Contains(step.Uses, "goreleaser/goreleaser-action") {
			goreleaserArgs = step.With["args"]
		}

		if step.Name == "Verify dev archives" {
			verifyScript = step.Run
		}
	}

	if !strings.Contains(goreleaserArgs, "-f .goreleaser-dev.yaml") {
		t.Fatalf("GoReleaser action does not use the dev config: %q", goreleaserArgs)
	}

	if verifyScript == "" {
		t.Fatal("dev-release workflow has no archive verification step")
	}

	for _, want := range []string{
		"test -x \"$notifier\"",
		"lipo \"$notifier\" -verify_arch arm64 x86_64",
		"codesign --verify --deep --strict \"$app\"",
		"GraithNotifier.app",
		"Linux dev archive unexpectedly contains",
	} {
		if !strings.Contains(verifyScript, want) {
			t.Errorf("archive verification step missing %q", want)
		}
	}
}

func TestDevHomebrewFormulaInstallsNotifierOnlyOnMacOS(t *testing.T) {
	workflow := loadDevReleaseWorkflow(t)
	job := workflow.Jobs["dev-release"]

	var script string

	for _, step := range job.Steps {
		if step.Name == "Update Homebrew tap" {
			script = step.Run
			break
		}
	}

	if script == "" {
		t.Fatal("dev-release workflow has no Homebrew formula generation step")
	}

	if strings.Contains(script, "sha256sum") {
		t.Error("macOS dev-release workflow still uses Linux-only sha256sum")
	}

	if !strings.Contains(script, "shasum -a 256") {
		t.Error("macOS dev-release workflow does not calculate SHA-256 with shasum")
	}

	if strings.Contains(script, "sed -i") {
		t.Error("macOS dev-release workflow still uses non-portable in-place sed")
	}

	const formulaStart = "cat > /tmp/graith-dev.rb << FORMULA\n"

	_, formulaAndRest, ok := strings.Cut(script, formulaStart)
	if !ok {
		t.Fatalf("Homebrew update step has no formula heredoc:\n%s", script)
	}

	formula, _, ok := strings.Cut(formulaAndRest, "\nFORMULA")
	if !ok {
		t.Fatalf("Homebrew formula heredoc has no terminator:\n%s", script)
	}

	if !strings.HasPrefix(formula, "# typed: false\n") {
		t.Fatalf("generated formula retains invalid leading heredoc indentation:\n%s", formula)
	}

	installAt := strings.Index(formula, "def install")
	if installAt < 0 {
		t.Fatalf("generated formula is missing the macOS notifier install:\n%s", formula)
	}

	installScript := formula[installAt:]
	macGuardAt := strings.Index(installScript, "if OS.mac?")

	appInstallAt := strings.Index(installScript, `(libexec/"graith").install "GraithNotifier.app"`)
	if macGuardAt < 0 || appInstallAt < 0 {
		t.Fatalf("generated formula is missing the macOS notifier install:\n%s", formula)
	}

	if macGuardAt > appInstallAt {
		t.Fatal("generated formula installs GraithNotifier.app outside its OS.mac? guard")
	}

	if strings.Count(formula, `(libexec/"graith").install "GraithNotifier.app"`) != 1 {
		t.Fatal("generated formula must contain exactly one notifier install")
	}
}

func TestProductionNotifierPackagingRemainsSeparated(t *testing.T) {
	cfg := loadGoreleaserConfig(t)
	linuxFiles := archiveByID(t, cfg, "linux")
	darwinFiles := archiveByID(t, cfg, "darwin")

	for _, file := range linuxFiles {
		if strings.Contains(file.path(), "GraithNotifier.app") {
			t.Fatalf("production Linux archive unexpectedly contains notifier mapping %q", file.path())
		}
	}

	for _, file := range darwinFiles {
		if file.Src == "macos/build/GraithNotifier.app/**/*" && file.Dst == "GraithNotifier.app/Contents" {
			return
		}
	}

	t.Fatal("production Darwin archive no longer carries GraithNotifier.app in the expected layout")
}
