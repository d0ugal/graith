package release

import (
	"os"
	"os/exec"
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
		ID      string   `yaml:"id"`
		Binary  string   `yaml:"binary"`
		Env     []string `yaml:"env"`
		Flags   []string `yaml:"flags"`
		Tags    []string `yaml:"tags"`
		Ldflags []string `yaml:"ldflags"`
		Hooks   struct {
			Post []struct {
				Cmd string `yaml:"cmd"`
			} `yaml:"post"`
		} `yaml:"hooks"`
		Goos   []string `yaml:"goos"`
		Goarch []string `yaml:"goarch"`
	} `yaml:"builds"`
	Archives []struct {
		ID           string        `yaml:"id"`
		IDs          []string      `yaml:"ids"`
		NameTemplate string        `yaml:"name_template"`
		Files        []archiveFile `yaml:"files"`
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

func TestDevGoReleaserSelectsNativeOnlyForDarwinArm64Canary(t *testing.T) {
	cfg := loadDevReleaseConfig(t)

	hasBuildHook := false
	hasFailClosedHook := false
	hasNativeArtifactGuard := false

	for _, hook := range cfg.Before.Hooks {
		hasBuildHook = hasBuildHook || strings.Contains(hook, "make notifier")
		hasFailClosedHook = hasFailClosedHook ||
			(strings.Contains(hook, `test "$(uname -s)" != Darwin`) &&
				strings.Contains(hook, "test -x macos/build/GraithNotifier.app/Contents/MacOS/graith-notifier"))
		hasNativeArtifactGuard = hasNativeArtifactGuard ||
			(strings.Contains(hook, "Darwin-arm64") &&
				strings.Contains(hook, "GRAITH_LIBGHOSTTY_WORK/pkgconfig/libghostty-vt-static.pc"))
	}

	if !hasBuildHook {
		t.Fatal("dev GoReleaser config does not build GraithNotifier.app")
	}

	if !hasFailClosedHook {
		t.Fatal("dev GoReleaser config does not fail closed when the Darwin notifier executable is missing")
	}

	if !hasNativeArtifactGuard {
		t.Fatal("dev GoReleaser config does not fail closed when the pinned native artifact is missing")
	}

	wantBuilds := map[string]struct {
		goos              []string
		goarch            []string
		cgo               string
		tags              []string
		managed           bool
		serviceHook       bool
		nativePackageHook bool
	}{
		"gr-dev-linux":              {goos: []string{"linux"}, goarch: []string{"amd64", "arm64"}, cgo: "CGO_ENABLED=0"},
		"gr-dev-darwin-amd64":       {goos: []string{"darwin"}, goarch: []string{"amd64"}, cgo: "CGO_ENABLED=0", managed: true, serviceHook: true},
		"gr-dev-darwin-arm64":       {goos: []string{"darwin"}, goarch: []string{"arm64"}, cgo: "CGO_ENABLED=1", tags: []string{"libghostty"}, managed: true, serviceHook: true, nativePackageHook: true},
		"gr-dev-darwin-arm64-charm": {goos: []string{"darwin"}, goarch: []string{"arm64"}, cgo: "CGO_ENABLED=0"},
	}

	if len(cfg.Builds) != len(wantBuilds) {
		t.Fatalf("dev GoReleaser builds = %d, want exactly %d", len(cfg.Builds), len(wantBuilds))
	}

	for id, want := range wantBuilds {
		found := false

		for _, build := range cfg.Builds {
			if build.ID != id {
				continue
			}

			found = true

			if build.Binary != "gr-dev" {
				t.Errorf("build %q binary = %q, want gr-dev", id, build.Binary)
			}

			if !slices.Equal(build.Goos, want.goos) {
				t.Errorf("build %q goos = %v, want %v", id, build.Goos, want.goos)
			}

			if !slices.Equal(build.Goarch, want.goarch) {
				t.Errorf("build %q goarch = %v, want %v", id, build.Goarch, want.goarch)
			}

			if !slices.Contains(build.Env, want.cgo) {
				t.Errorf("build %q env = %v, want %q", id, build.Env, want.cgo)
			}

			if !slices.Equal(build.Tags, want.tags) {
				t.Errorf("build %q tags = %v, want %v", id, build.Tags, want.tags)
			}

			if !slices.Contains(build.Flags, "-trimpath") {
				t.Errorf("build %q does not strip machine-local build paths", id)
			}

			flags := strings.Join(build.Ldflags, "\n")
			if want.managed {
				for _, required := range []string{
					"daemonservice.ManagedBuild={{ if eq (index .Env \"GRAITH_MANAGED_DEV_RELEASE\") \"true\" }}true{{ else }}false{{ end }}",
					"daemonservice.DevelopmentBuild=false",
					"daemonservice.ExpectedTeamID={{ if eq (index .Env \"GRAITH_MANAGED_DEV_RELEASE\") \"true\" }}{{ index .Env \"GRAITH_SIGNING_TEAM_ID\" }}{{ end }}",
					"daemonservice.ExpectedRequirementBase64={{ if eq (index .Env \"GRAITH_MANAGED_DEV_RELEASE\") \"true\" }}{{ index .Env \"GRAITH_SIGNING_REQUIREMENT_B64\" }}{{ end }}",
				} {
					if !strings.Contains(flags, required) {
						t.Errorf("Darwin dev ldflags missing %q", required)
					}
				}
			} else if strings.Contains(flags, "daemonservice.ManagedBuild=") {
				t.Errorf("Linux dev build unexpectedly enables the macOS service: flags=%q hooks=%#v", flags, build.Hooks.Post)
			}

			hookCommands := make([]string, 0, len(build.Hooks.Post))
			for _, hook := range build.Hooks.Post {
				hookCommands = append(hookCommands, hook.Cmd)
			}

			hooks := strings.Join(hookCommands, "\n")
			if strings.Contains(hooks, "macos/service/release-hook.sh") != want.serviceHook {
				t.Errorf("build %q service hook presence = %v, want %v", id, strings.Contains(hooks, "macos/service/release-hook.sh"), want.serviceHook)
			}

			if strings.Contains(hooks, "package-darwin-arm64") != want.nativePackageHook {
				t.Errorf("build %q native package hook presence = %v, want %v", id, strings.Contains(hooks, "package-darwin-arm64"), want.nativePackageHook)
			}

			if want.nativePackageHook {
				for _, required := range []string{"GRAITH_SPDX_VALIDATOR_JAR", "macos/build/libghostty-native-arm64", "gr-dev"} {
					if !strings.Contains(hooks, required) {
						t.Errorf("native package hook missing %q", required)
					}
				}
			}
		}

		if !found {
			t.Errorf("dev GoReleaser config has no %q build", id)
		}
	}

	for _, tc := range []struct {
		archiveID      string
		buildID        string
		wantNotifier   bool
		wantService    bool
		wantNativeMeta bool
		wantSuffix     string
	}{
		{archiveID: "linux", buildID: "gr-dev-linux"},
		{archiveID: "darwin-amd64", buildID: "gr-dev-darwin-amd64", wantNotifier: true, wantService: true},
		{archiveID: "darwin-arm64", buildID: "gr-dev-darwin-arm64", wantNotifier: true, wantService: true, wantNativeMeta: true},
		{archiveID: "darwin-arm64-charm", buildID: "gr-dev-darwin-arm64-charm", wantNotifier: true, wantSuffix: "_charm"},
	} {
		t.Run(tc.archiveID, func(t *testing.T) {
			for _, archive := range cfg.Archives {
				if archive.ID != tc.archiveID {
					continue
				}

				if !slices.Equal(archive.IDs, []string{tc.buildID}) {
					t.Fatalf("archive %q ids = %v, want [%s]", tc.archiveID, archive.IDs, tc.buildID)
				}

				hasNotifier := false
				hasService := false
				nativeMetadata := make(map[string]bool)

				for _, file := range archive.Files {
					switch {
					case strings.Contains(file.path(), "GraithNotifier.app"):
						hasNotifier = true

						if file.Src != "macos/build/GraithNotifier.app/**/*" || file.Dst != "GraithNotifier.app/Contents" {
							t.Errorf("notifier archive mapping = src %q dst %q", file.Src, file.Dst)
						}
					case strings.Contains(file.path(), "Graith.app"):
						hasService = true

						if file.Src != "macos/build/service-release-{{ .Arch }}/Graith.app/**/*" || file.Dst != "Graith.app/Contents" {
							t.Errorf("service archive mapping = src %q dst %q", file.Src, file.Dst)
						}
					case strings.HasSuffix(file.path(), "libghostty-native.spdx.json"):
						nativeMetadata["libghostty-native.spdx.json"] = true

						if file.Src != "macos/build/libghostty-native-arm64/libghostty-native.spdx.json" || file.Dst != "libghostty-native.spdx.json" {
							t.Errorf("native SPDX archive mapping = src %q dst %q", file.Src, file.Dst)
						}
					case strings.HasSuffix(file.path(), "THIRD_PARTY_NOTICES.libghostty.md"):
						nativeMetadata["THIRD_PARTY_NOTICES.libghostty.md"] = true

						if file.Src != "macos/build/libghostty-native-arm64/THIRD_PARTY_NOTICES.libghostty.md" || file.Dst != "THIRD_PARTY_NOTICES.libghostty.md" {
							t.Errorf("native notices archive mapping = src %q dst %q", file.Src, file.Dst)
						}
					}
				}

				if hasNotifier != tc.wantNotifier || hasService != tc.wantService {
					t.Errorf("archive %q apps: notifier=%v service=%v, want notifier=%v service=%v", tc.archiveID, hasNotifier, hasService, tc.wantNotifier, tc.wantService)
				}

				hasNativeMeta := len(nativeMetadata) == 2
				if hasNativeMeta != tc.wantNativeMeta {
					t.Errorf("archive %q native metadata = %v, want %v: %v", tc.archiveID, hasNativeMeta, tc.wantNativeMeta, nativeMetadata)
				}

				if tc.wantNativeMeta {
					for _, name := range []string{"libghostty-native.spdx.json", "THIRD_PARTY_NOTICES.libghostty.md"} {
						if !nativeMetadata[name] {
							t.Errorf("archive %q missing %s", tc.archiveID, name)
						}
					}
				}

				if !strings.HasSuffix(archive.NameTemplate, tc.wantSuffix) {
					t.Errorf("archive %q name template = %q, want suffix %q", tc.archiveID, archive.NameTemplate, tc.wantSuffix)
				}

				return
			}

			t.Fatalf("dev GoReleaser config has no %q archive", tc.archiveID)
		})
	}
}

func TestDevReleaseSigningModeRequiresAllOrNoCredentials(t *testing.T) {
	script := releaseRootPath("macos", "service", "release-signing-mode.sh")
	secretNames := []string{
		"MACOS_SIGNING_CERTIFICATE", "MACOS_SIGNING_CERTIFICATE_PASSWORD", "MACOS_SIGNING_IDENTITY",
		"MACOS_SIGNING_TEAM_ID", "MACOS_SIGNING_REQUIREMENT", "APPLE_NOTARY_PRIVATE_KEY",
		"APPLE_NOTARY_KEY_ID", "APPLE_NOTARY_ISSUER_ID",
	}

	completeCredentials := make([]string, 0, len(secretNames))
	for _, name := range secretNames {
		completeCredentials = append(completeCredentials, name+"=braw")
	}

	for _, test := range []struct {
		name        string
		environment []string
		wantOutput  string
		wantError   string
	}{
		{name: "absent credentials use legacy packaging", wantOutput: "disabled\n"},
		{
			name:        "complete credentials enable managed packaging",
			environment: completeCredentials,
			wantOutput:  "enabled\n",
		},
		{
			name:        "partial credentials fail closed",
			environment: []string{"MACOS_SIGNING_IDENTITY=braw"},
			wantError:   "partial macOS release-signing credentials; missing: MACOS_SIGNING_CERTIFICATE",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(script)

			command.Env = append([]string{"PATH=" + os.Getenv("PATH")}, test.environment...)

			output, err := command.CombinedOutput()
			if test.wantError != "" {
				if err == nil || !strings.Contains(string(output), test.wantError) {
					t.Fatalf("signing mode error = %v, output = %q, want %q", err, output, test.wantError)
				}

				if strings.Contains(string(output), "=braw") {
					t.Fatalf("signing mode leaked a credential value: %q", output)
				}

				return
			}

			if err != nil || string(output) != test.wantOutput {
				t.Fatalf("signing mode error = %v, output = %q, want %q", err, output, test.wantOutput)
			}
		})
	}
}

func TestNativeCandidatePackagingRejectsUnsafeDevBinaryName(t *testing.T) {
	work := t.TempDir()
	binary := filepath.Join(work, "braw")
	validator := filepath.Join(work, "canny.jar")
	destination := filepath.Join(work, "candidate")

	for _, path := range []string{binary, validator} {
		if err := os.WriteFile(path, []byte("dreich"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command(
		releaseRootPath("scripts", "libghostty-native.sh"),
		"package-darwin-arm64", binary, destination, validator, "../gr-dev",
	)

	command.Env = append(os.Environ(), "GRAITH_LIBGHOSTTY_WORK="+filepath.Join(work, "native"))

	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "[package-filename]") {
		t.Fatalf("unsafe package filename error = %v, output = %q", err, output)
	}

	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("unsafe package filename created destination: %v", err)
	}
}

type devReleaseWorkflow struct {
	Jobs map[string]struct {
		RunsOn string `yaml:"runs-on"`
		Steps  []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			If   string            `yaml:"if"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
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

	if job.RunsOn != "macos-26" {
		t.Fatalf("dev-release runner = %q, want macos-26 for native arm64 execution", job.RunsOn)
	}

	var (
		goreleaserArgs, prepareScript, signingScript, verifyScript, cleanupScript, cleanupIf string
		prepareEnv, signingEnv                                                               map[string]string
	)

	for _, step := range job.Steps {
		if strings.Contains(step.Uses, "goreleaser/goreleaser-action") {
			goreleaserArgs = step.With["args"]
		}

		if step.Name == "Verify dev archives" {
			verifyScript = step.Run
		}

		if step.Name == "Prepare and validate the pinned macOS arm64 backend" {
			prepareScript = step.Run
			prepareEnv = step.Env
		}

		if step.Name == "Configure optional macOS service signing" {
			signingScript = step.Run
			signingEnv = step.Env
		}

		if step.Name == "Remove temporary macOS signing keychain" {
			cleanupScript = step.Run
			cleanupIf = step.If
		}
	}

	if !strings.Contains(goreleaserArgs, "-f .goreleaser-dev.yaml") {
		t.Fatalf("GoReleaser action does not use the dev config: %q", goreleaserArgs)
	}

	if verifyScript == "" {
		t.Fatal("dev-release workflow has no archive verification step")
	}

	for _, want := range []string{
		`test "$(uname -s)-$(uname -m)" = Darwin-arm64`,
		"scripts/libghostty-native.sh test",
		"scripts/libghostty-native.sh test-metadata-policy",
		"scripts/libghostty-native.sh test-darwin-linkage-policy",
		"scripts/libghostty-native.sh test-exclusive-publish",
		"scripts/libghostty-native.sh install-spdx-validator",
		"scripts/libghostty-native.sh validate-spdx",
		"GRAITH_LIBGHOSTTY_WORK=", "PKG_CONFIG_PATH=", "GRAITH_SPDX_VALIDATOR_JAR=",
	} {
		if !strings.Contains(prepareScript, want) {
			t.Errorf("native preparation step missing %q", want)
		}
	}

	if !strings.Contains(prepareEnv["GRAITH_LIBGHOSTTY_WORK"], "runner.temp") || prepareEnv["GRAITH_LIBGHOSTTY_KEEP_WORK"] != "1" {
		t.Errorf("native preparation does not retain one runner-local canonical work directory: %#v", prepareEnv)
	}

	for _, secret := range []string{
		"MACOS_SIGNING_CERTIFICATE", "MACOS_SIGNING_CERTIFICATE_PASSWORD", "MACOS_SIGNING_IDENTITY",
		"MACOS_SIGNING_TEAM_ID", "MACOS_SIGNING_REQUIREMENT", "APPLE_NOTARY_PRIVATE_KEY",
		"APPLE_NOTARY_KEY_ID", "APPLE_NOTARY_ISSUER_ID",
	} {
		if !strings.Contains(signingEnv[secret], "secrets."+secret) {
			t.Errorf("dev signing step does not map %s from its repository secret", secret)
		}
	}

	for _, want := range []string{
		"release-signing-mode.sh", "publishing the legacy direct-spawn dev package",
		"GRAITH_MANAGED_DEV_RELEASE=false", "security import", "notarytool store-credentials",
		"GRAITH_SIGNING_REQUIREMENT_B64", "GRAITH_MANAGED_DEV_RELEASE=true", "GRAITH_SIGNED_SNAPSHOT=true",
	} {
		if !strings.Contains(signingScript, want) {
			t.Errorf("dev signing setup missing %q", want)
		}
	}

	for _, want := range []string{
		`case "${GRAITH_MANAGED_DEV_RELEASE:-false}"`,
		"test -x \"$notifier\"",
		"lipo \"$notifier\" -verify_arch arm64 x86_64",
		"codesign --verify --deep --strict \"$notifier_app\"",
		"GraithNotifier.app",
		"Graith.app",
		"cmp \"$unpacked/gr-dev\" \"$service_app/Contents/MacOS/gr\"",
		"macos/service/verify-release-archive.sh",
		"Legacy dev archive unexpectedly contains Graith.app",
		"Charm rollback archive unexpectedly contains Graith.app",
		"graith-dev_darwin_arm64_charm.tar.gz",
		"verify-darwin-arm64-candidate",
		"verify-candidate-spdx",
		"verify-default-binary",
		"GRAITH_SPDX_VALIDATOR_JAR",
		"Rollback archive unexpectedly contains native metadata",
		"Linux dev archive unexpectedly contains a macOS app",
	} {
		if !strings.Contains(verifyScript, want) {
			t.Errorf("archive verification step missing %q", want)
		}
	}

	if cleanupIf != "always()" || !strings.Contains(cleanupScript, `security delete-keychain "$GRAITH_RELEASE_KEYCHAIN"`) {
		t.Errorf("dev signing keychain cleanup is not fail-safe: if=%q run=%q", cleanupIf, cleanupScript)
	}
}

func TestDevHomebrewFormulaInstallsMacAppsOnlyOnMacOS(t *testing.T) {
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

	if strings.Contains(formula, "_charm") {
		t.Fatal("generated Homebrew formula selects the emergency Charm rollback instead of the native arm64 canary")
	}

	installAt := strings.Index(formula, "def install")
	if installAt < 0 {
		t.Fatalf("generated formula is missing the macOS notifier install:\n%s", formula)
	}

	installScript := formula[installAt:]
	macGuardAt := strings.Index(installScript, "if OS.mac?")

	for _, app := range []string{"GraithNotifier.app", "Graith.app"} {
		install := `(libexec/"graith").install "` + app + `"`

		appInstallAt := strings.Index(installScript, install)
		if macGuardAt < 0 || appInstallAt < 0 {
			t.Fatalf("generated formula is missing the macOS %s install:\n%s", app, formula)
		}

		if macGuardAt > appInstallAt {
			t.Fatalf("generated formula installs %s outside its OS.mac? guard", app)
		}

		if strings.Count(formula, install) != 1 {
			t.Fatalf("generated formula must contain exactly one %s install", app)
		}
	}

	serviceGuardAt := strings.Index(installScript, `(buildpath/"Graith.app").directory?`)

	serviceInstallAt := strings.Index(installScript, `(libexec/"graith").install "Graith.app"`)
	if serviceGuardAt < 0 || serviceGuardAt > serviceInstallAt {
		t.Fatal("generated formula does not gate the service app on archive presence")
	}

	for _, caveat := range []string{"gr-dev daemon restart", "gr-dev daemon service remove", "Before uninstalling on macOS"} {
		if !strings.Contains(formula, caveat) {
			t.Errorf("generated formula caveats missing %q", caveat)
		}
	}

	if !strings.Contains(formula, `return unless (libexec/"graith/Graith.app").directory?`) {
		t.Fatal("generated formula shows managed-service caveats for legacy dev packages")
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

func TestStableReleaseRemainsPureGoDuringDevCanary(t *testing.T) {
	data, err := os.ReadFile(releaseRootPath(".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var config struct {
		Builds []struct {
			ID   string   `yaml:"id"`
			Env  []string `yaml:"env"`
			Tags []string `yaml:"tags"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"gr-linux": false, "gr-darwin": false}
	for _, build := range config.Builds {
		if _, ok := want[build.ID]; !ok {
			continue
		}

		want[build.ID] = true
		if !slices.Contains(build.Env, "CGO_ENABLED=0") {
			t.Errorf("stable build %q is not explicitly pure Go: %v", build.ID, build.Env)
		}

		if slices.Contains(build.Tags, "libghostty") {
			t.Errorf("stable build %q unexpectedly selects libghostty", build.ID)
		}
	}

	for id, found := range want {
		if !found {
			t.Errorf("stable GoReleaser config has no %q build", id)
		}
	}
}
