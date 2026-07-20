package release

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

type serviceReleaseConfig struct {
	Builds []struct {
		ID      string   `yaml:"id"`
		Ldflags []string `yaml:"ldflags"`
		Hooks   struct {
			Post []struct {
				Cmd string `yaml:"cmd"`
			} `yaml:"post"`
		} `yaml:"hooks"`
	} `yaml:"builds"`
	Archives []struct {
		ID    string        `yaml:"id"`
		Files []archiveFile `yaml:"files"`
	} `yaml:"archives"`
	Brews []struct {
		Install string `yaml:"install"`
		Caveats string `yaml:"caveats"`
	} `yaml:"brews"`
	Signs []struct {
		ID        string   `yaml:"id"`
		Cmd       string   `yaml:"cmd"`
		Args      []string `yaml:"args"`
		Signature string   `yaml:"signature"`
		Artifacts string   `yaml:"artifacts"`
		IDs       []string `yaml:"ids"`
	} `yaml:"signs"`
}

func serviceRepoRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	return root
}

func loadServiceReleaseConfig(t *testing.T) serviceReleaseConfig {
	t.Helper()

	data, err := os.ReadFile(goreleaserConfigPath())
	if err != nil {
		t.Fatal(err)
	}

	var config serviceReleaseConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}

	return config
}

func TestDarwinReleaseBuildIsManagedAndPackagesMatchingApp(t *testing.T) {
	config := loadServiceReleaseConfig(t)

	var darwin, linux *struct {
		ID      string   `yaml:"id"`
		Ldflags []string `yaml:"ldflags"`
		Hooks   struct {
			Post []struct {
				Cmd string `yaml:"cmd"`
			} `yaml:"post"`
		} `yaml:"hooks"`
	}
	for index := range config.Builds {
		switch config.Builds[index].ID {
		case "gr-darwin":
			darwin = &config.Builds[index]
		case "gr-linux":
			linux = &config.Builds[index]
		}
	}

	if darwin == nil || linux == nil {
		t.Fatalf("missing OS release builds: %#v", config.Builds)
	}

	darwinFlags := strings.Join(darwin.Ldflags, "\n")
	for _, required := range []string{"ManagedBuild=true", "DevelopmentBuild={{ .IsSnapshot }}", "ExpectedTeamID=", "ExpectedRequirementBase64="} {
		if !strings.Contains(darwinFlags, required) {
			t.Errorf("Darwin ldflags missing %q", required)
		}

		if strings.Contains(strings.Join(linux.Ldflags, "\n"), required) {
			t.Errorf("Linux build unexpectedly enables managed service with %q", required)
		}
	}

	if len(darwin.Hooks.Post) != 1 || !strings.Contains(darwin.Hooks.Post[0].Cmd, "macos/service/release-hook.sh") || !strings.Contains(darwin.Hooks.Post[0].Cmd, "{{ .Path }}") || !strings.Contains(darwin.Hooks.Post[0].Cmd, "{{ .IsSnapshot }}") {
		t.Fatalf("Darwin build does not sign the completed artifact: %#v", darwin.Hooks.Post)
	}

	archiveFiles := make(map[string][]archiveFile)
	for _, archive := range config.Archives {
		archiveFiles[archive.ID] = archive.Files
	}

	hasServiceApp := func(files []archiveFile) bool {
		for _, file := range files {
			if strings.Contains(file.Src, "service-release-{{ .Arch }}/Graith.app") && file.Dst == "Graith.app/Contents" {
				return true
			}
		}

		return false
	}
	if !hasServiceApp(archiveFiles["darwin"]) {
		t.Fatal("Darwin archive does not carry its architecture-specific Graith.app")
	}

	if hasServiceApp(archiveFiles["linux"]) {
		t.Fatal("Linux archive unexpectedly carries Graith.app")
	}
}

func TestHomebrewInstallsServiceAppAndDocumentsExplicitUninstall(t *testing.T) {
	config := loadServiceReleaseConfig(t)
	if len(config.Brews) != 1 {
		t.Fatalf("brews = %d, want one", len(config.Brews))
	}

	brew := config.Brews[0]
	if !strings.Contains(brew.Install, `(libexec/"graith").install "Graith.app"`) {
		t.Fatal("Homebrew formula does not install Graith.app in the discovery layout")
	}

	if !strings.Contains(brew.Caveats, "gr daemon service remove --all-profiles") || !strings.Contains(strings.ToLower(brew.Caveats), "before uninstall") {
		t.Fatal("Homebrew caveats do not require explicit per-user service removal before uninstall")
	}
}

func TestStableWorkflowFailsClosedWithoutSigningAndNotaryInputs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(serviceRepoRoot(t), ".github", "workflows", "goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}

	workflow := string(data)
	for _, required := range []string{
		"MACOS_SIGNING_CERTIFICATE", "MACOS_SIGNING_CERTIFICATE_PASSWORD", "MACOS_SIGNING_IDENTITY",
		"MACOS_SIGNING_TEAM_ID", "MACOS_SIGNING_REQUIREMENT", "APPLE_NOTARY_PRIVATE_KEY",
		"APPLE_NOTARY_KEY_ID", "APPLE_NOTARY_ISSUER_ID", "Stable Darwin artifacts require signing and notarization",
		"Build, verify, and publish stable release", "archive-signing pipe", "args: release --clean",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("stable release workflow missing fail-closed input/check %q", required)
		}
	}

	if strings.Contains(workflow, "--skip=publish") || strings.Count(workflow, "args: release --clean") != 1 {
		t.Fatal("stable release rebuilds or discards verified Darwin archives")
	}

	config := loadServiceReleaseConfig(t)
	if len(config.Signs) != 1 {
		t.Fatalf("archive verification signs = %#v, want one", config.Signs)
	}

	verification := config.Signs[0]
	if verification.ID != "verify-darwin-service" || verification.Cmd != "macos/service/verify-release-archive.sh" || verification.Artifacts != "archive" || len(verification.IDs) != 1 || verification.IDs[0] != "darwin" || !strings.Contains(verification.Signature, "service-verified") {
		t.Fatalf("Darwin archive verification hook = %#v", verification)
	}

	if strings.Join(verification.Args, " ") != "${artifact} ${signature} {{ .IsSnapshot }}" {
		t.Fatalf("Darwin archive verification args = %q", verification.Args)
	}
}

func TestServiceBundleBuildContract(t *testing.T) {
	root := serviceRepoRoot(t)
	buildPath := filepath.Join(root, "macos", "service", "build.sh")

	info, err := os.Stat(buildPath)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm()&0o111 == 0 {
		t.Fatal("service build script is not executable")
	}

	buildData, err := os.ReadFile(buildPath)
	if err != nil {
		t.Fatal(err)
	}

	build := string(buildData)
	for _, required := range []string{
		"stable build refuses ad-hoc signing", "notarytool submit", "stapler staple", "stapler validate", "--options runtime",
		"spctl --assess", "codesign --verify --deep --strict", `cmp "$output/gr" "$output/Graith.app/Contents/MacOS/gr"`,
		"actual_requirement", "expected_requirement",
	} {
		if !strings.Contains(build, required) {
			t.Errorf("service build script missing %q", required)
		}
	}

	controllerData, err := os.ReadFile(filepath.Join(root, "macos", "service", "controller.swift"))
	if err != nil {
		t.Fatal(err)
	}

	controller := string(controllerData)
	for _, required := range []string{"kSMErrorLaunchDeniedByUser", "kSMErrorJobNotFound", "kSMErrorDomainFramework", "SMAppServiceErrorDomain", "register-fresh"} {
		if !strings.Contains(controller, required) {
			t.Errorf("service controller does not normalize %s", required)
		}
	}

	if strings.Contains(controller, "kSMErrorAlreadyRegistered") {
		t.Fatal("service controller must not normalize an already-registered race to candidate ownership")
	}

	if !strings.Contains(controller, `found \(statusName(service.status))`) {
		t.Fatal("service controller fresh-registration error does not interpolate the observed status")
	}

	if strings.Index(build, `codesign $sign_args --sign "$identity" "$macos_dir/gr"`) > strings.Index(build, `codesign $sign_args --sign "$identity" "$app"`) {
		t.Fatal("outer app is signed before its nested gr payload")
	}

	if strings.Index(build, `codesign $sign_args --sign "$identity" "$macos_dir/graith-service-controller"`) > strings.Index(build, `codesign $sign_args --sign "$identity" "$app"`) {
		t.Fatal("outer app is signed before its nested service controller")
	}

	verifyPath := filepath.Join(root, "macos", "service", "verify-release-archive.sh")

	verifyInfo, err := os.Stat(verifyPath)
	if err != nil {
		t.Fatal(err)
	}

	if verifyInfo.Mode().Perm()&0o111 == 0 {
		t.Fatal("Darwin archive verification script is not executable")
	}

	verifyData, err := os.ReadFile(verifyPath)
	if err != nil {
		t.Fatal(err)
	}

	verify := string(verifyData)
	for _, required := range []string{"Graith.app/Contents/Info.plist", "LaunchAgents", "-eq 65", "LSUIElement", "codesign --verify --deep --strict", "stapler validate", "spctl --assess", `cmp "$standalone" "$app/Contents/MacOS/gr"`, "GRAITH_SIGNING_REQUIREMENT"} {
		if !strings.Contains(verify, required) {
			t.Errorf("Darwin archive verification script missing %q", required)
		}
	}

	plist, err := os.ReadFile(filepath.Join(root, "macos", "service", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}

	text := string(plist)
	if !strings.Contains(text, "<string>net.graith.service</string>") || !strings.Contains(text, "<key>LSUIElement</key>\n\t<true/>") {
		t.Fatal("Graith.app does not carry the fixed headless bundle identity")
	}
}

func TestServiceReleaseHookSeparatesSignedSnapshotsAndBuildIdentities(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(serviceRepoRoot(t), "macos", "service", "release-hook.sh"))
	if err != nil {
		t.Fatal(err)
	}

	text := string(data)
	for _, required := range []string{
		"GRAITH_SIGNED_SNAPSHOT", "include_service", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT",
		"GRAITH_BUNDLE_BUILD_NUMBER", "GRAITH_MACOS_SIGNING_IDENTITY",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("service release hook missing %q", required)
		}
	}

	if !strings.Contains(text, `[ "$snapshot" = true ] && [ "$signed_snapshot" = false ]`) {
		t.Fatal("service release hook no longer limits ad-hoc signing to unsigned snapshots")
	}
}

func TestServiceReleaseHookCanOmitDevServiceBundle(t *testing.T) {
	root := serviceRepoRoot(t)

	hook, err := os.ReadFile(filepath.Join(root, "macos", "service", "release-hook.sh"))
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	hookPath := filepath.Join(work, "release-hook.sh")
	writeTestExecutable(t, hookPath, hook)

	buildMarker := filepath.Join(work, "build-ran")
	writeTestExecutable(t, filepath.Join(work, "build.sh"), []byte("#!/bin/sh\ntouch \"$BUILD_MARKER\"\n"))

	staleOutput := filepath.Join(work, "macos", "build", "service-release-arm64")
	if err := os.MkdirAll(staleOutput, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(staleOutput, "Graith.app"), []byte("dreich\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	artifact := filepath.Join(work, "gr-dev")
	writeTestExecutable(t, artifact, []byte("canny\n"))
	command := exec.Command(hookPath, artifact, "darwin_arm64", "0.70.0-dev.1", "braw", "true", "false")
	command.Dir = work
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "BUILD_MARKER=" + buildMarker}

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release hook: %v: %s", err, output)
	}

	if _, err := os.Stat(buildMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy release unexpectedly ran service build: %v", err)
	}

	if _, err := os.Stat(staleOutput); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy release retained stale service output: %v", err)
	}

	data, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "canny\n" {
		t.Fatalf("legacy release changed standalone artifact: %q", data)
	}
}

func TestServiceReleaseHookRoutesSnapshotSigningAndUniqueBuildNumber(t *testing.T) {
	root := serviceRepoRoot(t)

	hook, err := os.ReadFile(filepath.Join(root, "macos", "service", "release-hook.sh"))
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		environment []string
		wantArgs    []string
		forbidArgs  []string
		wantError   string
	}{
		{
			name: "local snapshot stays ad hoc",
			environment: []string{
				"GRAITH_SIGNED_SNAPSHOT=false", "GITHUB_RUN_ID=1473", "GITHUB_RUN_ATTEMPT=2",
			},
			wantArgs: []string{"--build-number\n1473.2\n", "--development\n"},
		},
		{
			name: "channel snapshot uses distribution identity",
			environment: []string{
				"GRAITH_SIGNED_SNAPSHOT=true", "GITHUB_RUN_ID=1478", "GITHUB_RUN_ATTEMPT=3",
				"GRAITH_MACOS_SIGNING_IDENTITY=Developer ID Application: Braw",
				"GRAITH_NOTARY_PROFILE=canny", "GRAITH_SIGNING_TEAM_ID=BRAWTEAM",
				"GRAITH_SIGNING_REQUIREMENT=identifier net.graith.service",
			},
			wantArgs:   []string{"--build-number\n1478.3\n", "--identity\nDeveloper ID Application: Braw\n", "--notary-profile\ncanny\n"},
			forbidArgs: []string{"--development\n"},
		},
		{
			name:        "signed snapshot fails closed without credentials",
			environment: []string{"GRAITH_SIGNED_SNAPSHOT=true"},
			wantError:   "missing GRAITH_MACOS_SIGNING_IDENTITY",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := t.TempDir()
			hookPath := filepath.Join(work, "release-hook.sh")

			writeTestExecutable(t, hookPath, hook)

			argsPath := filepath.Join(work, "braw-args")

			mock := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$HOOK_ARGS"
payload=""
output=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		--payload) payload="$2"; shift 2 ;;
		--output) output="$2"; shift 2 ;;
		*) shift ;;
	esac
done
mkdir -p "$output/Graith.app/Contents/MacOS"
cp "$payload" "$output/gr"
cp "$payload" "$output/Graith.app/Contents/MacOS/gr"
`
			writeTestExecutable(t, filepath.Join(work, "build.sh"), []byte(mock))

			artifact := filepath.Join(work, "gr-dev")

			writeTestExecutable(t, artifact, []byte("dreich\n"))

			command := exec.Command(hookPath, artifact, "darwin_arm64", "0.70.0-dev.1", "canny", "true")
			command.Dir = work

			command.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOOK_ARGS=" + argsPath}, test.environment...)

			output, runErr := command.CombinedOutput()
			if test.wantError != "" {
				if runErr == nil || !strings.Contains(string(output), test.wantError) {
					t.Fatalf("release hook error = %v, output = %q, want %q", runErr, output, test.wantError)
				}

				return
			}

			if runErr != nil {
				t.Fatalf("release hook: %v: %s", runErr, output)
			}

			args, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}

			for _, want := range test.wantArgs {
				if !strings.Contains(string(args), want) {
					t.Errorf("build args %q missing %q", args, want)
				}
			}

			for _, forbidden := range test.forbidArgs {
				if strings.Contains(string(args), forbidden) {
					t.Errorf("build args %q contain forbidden %q", args, forbidden)
				}
			}
		})
	}
}

func writeTestExecutable(t *testing.T, path string, data []byte) {
	t.Helper()

	// #nosec G703 -- callers provide paths rooted in the test's t.TempDir().
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// #nosec G302 -- test-only executables are isolated inside t.TempDir().
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
