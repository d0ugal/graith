package release

import (
	"os"
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

func TestDevelopmentReleaseRemainsExplicitlyUnmanaged(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(serviceRepoRoot(t), ".goreleaser-dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	text := string(data)
	if strings.Contains(text, "ManagedBuild=true") || strings.Contains(text, "Graith.app") {
		t.Fatal("Linux-built development release silently claims managed macOS service support")
	}
}
