package release

import (
	"fmt"
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

func TestDevGoReleaserBuildsOnlyDarwinWithoutRollbackArchives(t *testing.T) {
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
		"gr-dev-darwin-amd64": {goos: []string{"darwin"}, goarch: []string{"amd64"}, cgo: "CGO_ENABLED=0", managed: true, serviceHook: true},
		"gr-dev-darwin-arm64": {goos: []string{"darwin"}, goarch: []string{"arm64"}, cgo: "CGO_ENABLED=1", tags: []string{"libghostty"}, managed: true, serviceHook: true, nativePackageHook: true},
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
		{archiveID: "darwin-amd64", buildID: "gr-dev-darwin-amd64", wantNotifier: true, wantService: true},
		{archiveID: "darwin-arm64", buildID: "gr-dev-darwin-arm64", wantNotifier: true, wantService: true, wantNativeMeta: true},
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

	if strings.Contains(string(mustReadReleaseFile(t, ".goreleaser-dev.yaml")), "_charm") {
		t.Fatal("dev GoReleaser config still declares a separately named Charm archive")
	}
}

func mustReadReleaseFile(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(releaseRootPath(name))
	if err != nil {
		t.Fatal(err)
	}

	return data
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

func TestCandidateSPDXBindsLinuxTargetAndExactBytes(t *testing.T) {
	work := t.TempDir()
	binary := filepath.Join(work, "gr-dev")
	document := filepath.Join(work, "candidate.spdx.json")
	revision := strings.Repeat("a", 40)

	if err := os.WriteFile(binary, []byte("dreich\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// #nosec G302 -- this test-only candidate must be owner-executable.
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}

	script := releaseRootPath("scripts", "libghostty-native.sh")

	command := exec.Command(script, "materialize-candidate-spdx", binary, revision, "linux", "arm64", document, "gr-dev")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("materialize Linux candidate SPDX: %v: %s", err, output)
	}

	materialized, err := os.ReadFile(document)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"-Demit-lib-vt=true -Demit-xcframework=false -Doptimize=ReleaseFast -Dtarget=aarch64-linux-gnu",
		"no Apple archive is used",
	} {
		if !strings.Contains(string(materialized), want) {
			t.Errorf("Linux candidate SPDX does not describe %q", want)
		}
	}

	for _, unwanted := range []string{"-Demit-xcframework=true", "The Apple archive SHA-256"} {
		if strings.Contains(string(materialized), unwanted) {
			t.Errorf("Linux candidate SPDX retains Apple build metadata %q", unwanted)
		}
	}

	command = exec.Command(script, "verify-target-candidate-spdx", binary, revision, "linux", "arm64", document, "gr-dev")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("verify Linux candidate SPDX: %v: %s", err, output)
	}

	appleMetadata := filepath.Join(work, "apple-metadata.spdx.json")

	changedMetadata := strings.Replace(
		string(materialized),
		"-Demit-xcframework=false",
		"-Demit-xcframework=true",
		1,
	)
	// #nosec G703 -- appleMetadata is confined to the test's t.TempDir.
	if err := os.WriteFile(appleMetadata, []byte(changedMetadata), 0o600); err != nil {
		t.Fatal(err)
	}

	command = exec.Command(script, "verify-target-candidate-spdx", binary, revision, "linux", "arm64", appleMetadata, "gr-dev")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("Linux candidate SPDX accepted Apple build metadata: %s", output)
	}

	file, err := os.OpenFile(binary, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := file.WriteString("thrawn\n"); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close changed candidate after write failure: %v", closeErr)
		}

		t.Fatal(err)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	command = exec.Command(script, "verify-target-candidate-spdx", binary, revision, "linux", "arm64", document, "gr-dev")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("candidate SPDX accepted changed binary bytes: %s", output)
	}
}

func TestDevReleaseVersionIsSharedMonotonicSnapshot(t *testing.T) {
	script := releaseRootPath("scripts", "dev-release-version.sh")

	for _, test := range []struct {
		name    string
		base    string
		epoch   string
		want    string
		wantErr string
	}{
		{name: "increments patch", base: "v0.69.6", epoch: "1784712345", want: "0.69.7-dev.1784712345\n"},
		{name: "rejects prerelease base", base: "v0.70.0-rc.1", epoch: "1784712345", wantErr: "vMAJOR.MINOR.PATCH"},
		{name: "rejects nonpositive epoch", base: "v0.69.6", epoch: "0", wantErr: "positive integer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := exec.Command(script, test.base, test.epoch).CombinedOutput()
			if test.wantErr != "" {
				if err == nil || !strings.Contains(string(output), test.wantErr) {
					t.Fatalf("version error = %v, output = %q, want %q", err, output, test.wantErr)
				}

				return
			}

			if err != nil || string(output) != test.want {
				t.Fatalf("version error = %v, output = %q, want %q", err, output, test.want)
			}
		})
	}
}

type devReleaseWorkflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	If   string            `yaml:"if"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
}

type devReleaseWorkflowJob struct {
	RunsOn      string            `yaml:"runs-on"`
	If          string            `yaml:"if"`
	Needs       workflowNeeds     `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Strategy    struct {
		Matrix struct {
			Include []map[string]string `yaml:"include"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []devReleaseWorkflowStep `yaml:"steps"`
}

type workflowNeeds []string

func (needs *workflowNeeds) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var value string
		if err := node.Decode(&value); err != nil {
			return err
		}

		*needs = []string{value}

		return nil
	case yaml.SequenceNode:
		return node.Decode((*[]string)(needs))
	default:
		return fmt.Errorf("workflow needs must be a scalar or sequence, got YAML kind %d", node.Kind)
	}
}

type devReleaseWorkflow struct {
	Events      map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress string `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]devReleaseWorkflowJob `yaml:"jobs"`
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

func workflowStep(job devReleaseWorkflowJob, name string) devReleaseWorkflowStep {
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}

	return devReleaseWorkflowStep{}
}

func TestDevReleaseWorkflowBuildsAndAggregatesPlatformArtifacts(t *testing.T) {
	workflow := loadDevReleaseWorkflow(t)
	if len(workflow.Events) != 2 {
		t.Errorf("dev release events = %#v, want only push and pull_request", workflow.Events)
	}

	for _, event := range []string{"push", "pull_request"} {
		if _, ok := workflow.Events[event]; !ok {
			t.Errorf("dev release workflow has no %q event", event)
		}
	}

	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		t.Errorf("dev release top-level permissions are not read-only: %#v", workflow.Permissions)
	}

	if workflow.Concurrency.Group != "dev-release-${{ github.event_name == 'push' && 'publish' || github.event.pull_request.number }}" ||
		workflow.Concurrency.CancelInProgress != "${{ github.event_name == 'pull_request' }}" {
		t.Errorf("dev release concurrency policy = %#v", workflow.Concurrency)
	}

	for _, name := range []string{"release-context", "build-darwin", "build-linux", "attest-linux", "execute-linux", "assemble-dev", "publish-dev"} {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Errorf("dev-release workflow has no %q job", name)
		}
	}

	wantNeeds := map[string]workflowNeeds{
		"release-context": nil,
		"build-darwin":    {"release-context"},
		"build-linux":     {"release-context"},
		"execute-linux":   {"release-context", "build-linux"},
		"attest-linux":    {"release-context", "build-linux", "execute-linux"},
		"assemble-dev":    {"release-context", "build-darwin", "build-linux", "execute-linux"},
		"publish-dev":     {"release-context", "assemble-dev", "attest-linux"},
	}
	for jobName, needs := range wantNeeds {
		if actual := workflow.Jobs[jobName].Needs; !slices.Equal(actual, needs) {
			t.Errorf("%s needs = %#v, want %#v", jobName, actual, needs)
		}
	}

	contextJob := workflow.Jobs["release-context"]

	contextScript := workflowStep(contextJob, "Bind one version and revision to every platform job").Run
	for _, want := range []string{"scripts/dev-release-base-tag.sh", "scripts/dev-release-version.sh", `test "$revision" = "$GITHUB_SHA"`, `>> "$GITHUB_OUTPUT"`} {
		if !strings.Contains(contextScript, want) {
			t.Errorf("release context step missing %q", want)
		}
	}

	darwinJob := workflow.Jobs["build-darwin"]
	if darwinJob.RunsOn != "macos-26" {
		t.Fatalf("Darwin release runner = %q, want macos-26", darwinJob.RunsOn)
	}

	prepareStep := workflowStep(darwinJob, "Prepare and validate the pinned macOS arm64 backend")
	for _, want := range []string{
		`test "$(uname -s)-$(uname -m)" = Darwin-arm64`,
		"scripts/libghostty-native.sh test", "test-metadata-policy",
		"test-darwin-linkage-policy", "test-exclusive-publish",
		"install-spdx-validator", "validate-spdx", "GRAITH_SPDX_VALIDATOR_JAR=",
	} {
		if !strings.Contains(prepareStep.Run, want) {
			t.Errorf("Darwin native preparation missing %q", want)
		}
	}

	if !strings.Contains(prepareStep.Env["GRAITH_LIBGHOSTTY_WORK"], "runner.temp") || prepareStep.Env["GRAITH_LIBGHOSTTY_KEEP_WORK"] != "1" {
		t.Errorf("Darwin preparation does not retain one canonical work directory: %#v", prepareStep.Env)
	}

	signingStep := workflowStep(darwinJob, "Configure optional macOS service signing")
	unsignedScript := workflowStep(darwinJob, "Configure unsigned pull-request packaging").Run

	var signingIf, unsignedIf string

	for _, step := range darwinJob.Steps {
		switch step.Name {
		case "Configure optional macOS service signing":
			signingIf = step.If
		case "Configure unsigned pull-request packaging":
			unsignedIf = step.If
		}
	}

	if signingIf != "github.event_name == 'push'" || unsignedIf != "github.event_name == 'pull_request'" {
		t.Errorf("signing secrets are not isolated from pull requests: signing=%q unsigned=%q", signingIf, unsignedIf)
	}

	for _, want := range []string{"GRAITH_MANAGED_DEV_RELEASE=false", "GRAITH_SIGNED_SNAPSHOT=false"} {
		if !strings.Contains(unsignedScript, want) {
			t.Errorf("unsigned pull-request setup missing %q", want)
		}
	}

	for _, secret := range []string{
		"MACOS_SIGNING_CERTIFICATE", "MACOS_SIGNING_CERTIFICATE_PASSWORD", "MACOS_SIGNING_IDENTITY",
		"MACOS_SIGNING_TEAM_ID", "MACOS_SIGNING_REQUIREMENT", "APPLE_NOTARY_PRIVATE_KEY",
		"APPLE_NOTARY_KEY_ID", "APPLE_NOTARY_ISSUER_ID",
	} {
		if !strings.Contains(signingStep.Env[secret], "secrets."+secret) {
			t.Errorf("dev signing step does not map %s from its repository secret", secret)
		}
	}

	for _, want := range []string{
		"release-signing-mode.sh", "GRAITH_MANAGED_DEV_RELEASE=false", "security import",
		"notarytool store-credentials", "GRAITH_SIGNING_REQUIREMENT_B64",
		"GRAITH_MANAGED_DEV_RELEASE=true", "GRAITH_SIGNED_SNAPSHOT=true",
	} {
		if !strings.Contains(signingStep.Run, want) {
			t.Errorf("dev signing setup missing %q", want)
		}
	}

	var (
		goreleaserUses string
		goreleaserWith map[string]string
	)

	for _, step := range darwinJob.Steps {
		if strings.Contains(step.Uses, "goreleaser/goreleaser-action") {
			goreleaserUses = step.Uses
			goreleaserWith = step.With
		}
	}

	if goreleaserUses == "" || !strings.Contains(goreleaserWith["args"], "-f .goreleaser-dev.yaml") {
		t.Fatalf("Darwin job does not run the dev GoReleaser config: %q %#v", goreleaserUses, goreleaserWith)
	}

	darwinVerify := workflowStep(darwinJob, "Verify Darwin dev archives").Run
	for _, want := range []string{
		`[ "${#all_archives[@]}" -ne 2 ]`, "separately named rollback archive",
		"verify-darwin-arm64-candidate", "verify-candidate-spdx", "verify-default-binary",
		"macos/service/verify-release-archive.sh", "GRAITH_SPDX_VALIDATOR_JAR",
	} {
		if !strings.Contains(darwinVerify, want) {
			t.Errorf("Darwin archive verification missing %q", want)
		}
	}

	if strings.Contains(darwinVerify, "darwin_arm64_charm") {
		t.Fatal("Darwin verification still expects the removed rollback archive")
	}

	cleanupScript := workflowStep(darwinJob, "Remove temporary macOS signing keychain").Run

	var cleanupIf string

	for _, step := range darwinJob.Steps {
		if step.Name == "Remove temporary macOS signing keychain" {
			cleanupIf = step.If
		}
	}

	if cleanupIf != "always()" || !strings.Contains(cleanupScript, `security delete-keychain "$GRAITH_RELEASE_KEYCHAIN"`) {
		t.Errorf("Darwin signing keychain cleanup is not fail-safe: if=%q run=%q", cleanupIf, cleanupScript)
	}

	linuxJob := workflow.Jobs["build-linux"]
	if linuxJob.RunsOn != "ubuntu-24.04" {
		t.Fatalf("Linux build runner = %q, want ubuntu-24.04", linuxJob.RunsOn)
	}

	wantTargets := map[string]string{"amd64": "x86_64-linux-gnu", "arm64": "aarch64-linux-gnu"}
	for _, entry := range linuxJob.Strategy.Matrix.Include {
		if wantTargets[entry["goarch"]] != entry["target"] {
			t.Errorf("unexpected Linux build matrix entry: %#v", entry)
		}

		delete(wantTargets, entry["goarch"])
	}

	if len(wantTargets) != 0 {
		t.Fatalf("Linux build matrix missing targets: %#v", wantTargets)
	}

	linuxBuild := workflowStep(linuxJob, "Build the exact pinned Linux dependency unit").Run
	for _, want := range []string{"source-build", "verify-static-archive", "test-source-archive-policy", "verify-dependency-unit", "verify-generated-dependency-unit"} {
		if !strings.Contains(linuxBuild, want) {
			t.Errorf("Linux dependency build missing %q", want)
		}
	}

	linuxPackage := workflowStep(linuxJob, "Build and verify the final Linux release archive").Run
	for _, want := range []string{
		"CGO_ENABLED=1", "-tags=libghostty", "--strip-debug", "package-linux",
		"libghostty-native.spdx.json", "THIRD_PARTY_NOTICES.libghostty.md",
		"tar --sort=name", "gzip --no-name", "verify-linux-dev-archive",
		"archive_sha", "binary_sha", "dev-linux-${GOARCH}-manifest.json",
	} {
		if !strings.Contains(linuxPackage, want) {
			t.Errorf("Linux final archive build missing %q", want)
		}
	}

	if linuxJob.Permissions["contents"] != "read" || linuxJob.Permissions["attestations"] != "" || linuxJob.Permissions["id-token"] != "" {
		t.Errorf("pull-request Linux builder has mutation permissions: %#v", linuxJob.Permissions)
	}

	attestJob := workflow.Jobs["attest-linux"]

	attestStep := workflowStep(attestJob, "Attest the final Linux release archive")
	if !strings.Contains(attestStep.Uses, "actions/attest@") || !strings.Contains(attestStep.With["subject-path"], "graith-dev_linux_") {
		t.Errorf("Linux final archive is not provenance-attested: uses=%q with=%#v", attestStep.Uses, attestStep.With)
	}

	if attestJob.If != "github.event_name == 'push'" || attestJob.Permissions["attestations"] != "write" || attestJob.Permissions["id-token"] != "write" {
		t.Errorf("Linux attestation is not isolated to push with required permissions: if=%q permissions=%#v", attestJob.If, attestJob.Permissions)
	}

	for _, need := range []string{"release-context", "build-linux", "execute-linux"} {
		if !slices.Contains(attestJob.Needs, need) {
			t.Errorf("Linux attestation does not wait for %q: needs=%#v", need, attestJob.Needs)
		}
	}

	executeJob := workflow.Jobs["execute-linux"]

	wantRunners := map[string]string{"amd64": "ubuntu-24.04", "arm64": "ubuntu-24.04-arm"}
	for _, entry := range executeJob.Strategy.Matrix.Include {
		if wantRunners[entry["goarch"]] != entry["runner"] {
			t.Errorf("unexpected Linux execution matrix entry: %#v", entry)
		}

		delete(wantRunners, entry["goarch"])
	}

	if len(wantRunners) != 0 {
		t.Fatalf("Linux execution matrix missing actual-architecture runners: %#v", wantRunners)
	}

	executeScript := workflowStep(executeJob, "Reverify and execute the published Linux bytes").Run
	for _, want := range []string{"sha256sum --check", "install-spdx-validator", "verify-linux-dev-archive", `"$spdx_jar" true`} {
		if !strings.Contains(executeScript, want) {
			t.Errorf("Linux actual-architecture execution missing %q", want)
		}
	}

	nativeVerifier := string(mustReadReleaseFile(t, "scripts/libghostty-native.sh"))
	if !strings.Contains(nativeVerifier, `"$binary" --graith-internal-libghostty-self-test`) {
		t.Error("Linux archive execution does not invoke the final binary's native terminal self-test")
	}

	for _, want := range []string{
		`host_goos="$(go env GOHOSTOS)"`,
		`host_goarch="$(go env GOHOSTARCH)"`,
		`GOOS="$host_goos" GOARCH="$host_goarch" CGO_ENABLED=0`,
	} {
		if !strings.Contains(nativeVerifier, want) {
			t.Errorf("atomic publication helper is not pinned to the build host with %q", want)
		}
	}

	nativeSelfTest := string(mustReadReleaseFile(t, "internal/pty/terminal_selftest.go"))
	for _, want := range []string{"newTerminal", "term.Write", "snapshotTerminal", "term.Resize", "term.Close"} {
		if !strings.Contains(nativeSelfTest, want) {
			t.Errorf("native terminal self-test does not exercise %q", want)
		}
	}

	assembleJob := workflow.Jobs["assemble-dev"]
	if assembleJob.Permissions["contents"] != "read" {
		t.Errorf("pull-request aggregation has mutation permissions: %#v", assembleJob.Permissions)
	}

	if !slices.Contains(assembleJob.Needs, "execute-linux") {
		t.Errorf("release aggregation does not wait for actual-architecture execution: needs=%#v", assembleJob.Needs)
	}

	aggregateScript := workflowStep(assembleJob, "Verify the complete same-commit release set").Run
	for _, want := range []string{
		`test "$(git rev-parse HEAD)" = "$RELEASE_REVISION"`,
		"graith-dev_linux_amd64.tar.gz", "graith-dev_linux_arm64.tar.gz",
		"dev-darwin-manifest.json", "dev-linux-${goarch}-manifest.json",
		"sha256sum --check", "tar -xOzf", "checksums.txt", `-eq 4`,
	} {
		if !strings.Contains(aggregateScript, want) {
			t.Errorf("controlled publisher aggregation missing %q", want)
		}
	}

	publishJob := workflow.Jobs["publish-dev"]
	if publishJob.If != "github.event_name == 'push'" || publishJob.Permissions["contents"] != "write" {
		t.Errorf("publisher is not isolated to push with write permission: if=%q permissions=%#v", publishJob.If, publishJob.Permissions)
	}

	provenanceScript := workflowStep(publishJob, "Verify Linux build provenance").Run
	for _, want := range []string{"gh attestation verify", "--signer-workflow", `--source-digest "$RELEASE_REVISION"`, "--source-ref refs/heads/main"} {
		if !strings.Contains(provenanceScript, want) {
			t.Errorf("publisher provenance verification missing %q", want)
		}
	}

	for _, need := range []string{"release-context", "assemble-dev", "attest-linux"} {
		if !slices.Contains(publishJob.Needs, need) {
			t.Errorf("publisher does not wait for %q: needs=%#v", need, publishJob.Needs)
		}
	}

	publishScript := workflowStep(publishJob, "Upload dev release").Run
	if strings.Contains(publishScript, "dist/*.tar.gz") || strings.Contains(publishScript, "_charm") {
		t.Fatal("publisher uses an open archive glob or rollback asset")
	}

	for _, want := range []string{
		`gh release delete dev --yes --repo "$GITHUB_REPOSITORY"`,
		`gh release create dev --repo "$GITHUB_REPOSITORY"`,
		`gh release view dev --repo "$GITHUB_REPOSITORY"`,
	} {
		if !strings.Contains(publishScript, want) {
			t.Errorf("checkout-free publisher does not explicitly select its repository with %q", want)
		}
	}

	for _, want := range []string{"--target \"$RELEASE_REVISION\"", "checksums.txt", "--json assets", "git/ref/heads/main", `"$current_main" != "$RELEASE_REVISION"`} {
		if !strings.Contains(publishScript, want) {
			t.Errorf("publisher final release step missing %q", want)
		}
	}

	var (
		mainGuardAt       = strings.Index(publishScript, "git/ref/heads/main")
		releaseMutationAt = strings.Index(publishScript, "gh release delete dev")
	)

	if mainGuardAt < 0 || releaseMutationAt < 0 || mainGuardAt > releaseMutationAt {
		t.Error("publisher does not verify the current main tip before release mutation")
	}

	workflowText := string(mustReadReleaseFile(t, ".github/workflows/dev-release.yml"))
	for _, want := range []string{"pull_request:", "cancel-in-progress: ${{ github.event_name == 'pull_request' }}", "github.run_id"} {
		if !strings.Contains(workflowText, want) {
			t.Errorf("dev release workflow policy missing %q", want)
		}
	}

	if strings.Contains(workflowText, "github.run_attempt") {
		t.Error("dev release artifact names change across partial workflow retries")
	}

	for jobName, stepName := range map[string]string{
		"build-darwin": "Upload verified Darwin archives",
		"build-linux":  "Upload verified Linux archive",
		"assemble-dev": "Upload assembled dev release for verification",
	} {
		uploadStep := workflowStep(workflow.Jobs[jobName], stepName)
		if uploadStep.With["overwrite"] != "true" {
			t.Errorf("%s artifact upload is not safe to rerun: with=%#v", jobName, uploadStep.With)
		}
	}
}

func TestDevHomebrewFormulaInstallsMacAppsOnlyOnMacOS(t *testing.T) {
	workflow := loadDevReleaseWorkflow(t)
	job := workflow.Jobs["publish-dev"]

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

	if !strings.Contains(script, "sha256sum") {
		t.Error("Linux publisher does not calculate final archive SHA-256 values")
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
		t.Fatal("generated Homebrew formula refers to a separately named rollback archive")
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

func TestReleaseConfigsDoNotPublishSeparatelyNamedRollbackArchives(t *testing.T) {
	tests := []struct {
		name         string
		wantBuilds   []string
		wantArchives []string
	}{
		{
			name:         ".goreleaser-dev.yaml",
			wantBuilds:   []string{"gr-dev-darwin-amd64", "gr-dev-darwin-arm64"},
			wantArchives: []string{"darwin-amd64", "darwin-arm64"},
		},
		{
			name:         ".goreleaser.yaml",
			wantBuilds:   []string{"gr-linux", "gr-darwin"},
			wantArchives: []string{"linux", "darwin"},
		},
	}

	for _, test := range tests {
		var config struct {
			Builds []struct {
				ID string `yaml:"id"`
			} `yaml:"builds"`
			Archives []struct {
				ID           string `yaml:"id"`
				NameTemplate string `yaml:"name_template"`
			} `yaml:"archives"`
		}
		if err := yaml.Unmarshal(mustReadReleaseFile(t, test.name), &config); err != nil {
			t.Fatalf("parse %s: %v", test.name, err)
		}

		if len(config.Builds) != len(test.wantBuilds) {
			t.Errorf("%s builds = %d, want exact set %#v", test.name, len(config.Builds), test.wantBuilds)
		}

		if len(config.Archives) != len(test.wantArchives) {
			t.Errorf("%s archives = %d, want exact set %#v", test.name, len(config.Archives), test.wantArchives)
		}

		for _, build := range config.Builds {
			if !slices.Contains(test.wantBuilds, build.ID) {
				t.Errorf("%s declares unexpected alternate build %q", test.name, build.ID)
			}

			lower := strings.ToLower(build.ID)
			if strings.Contains(lower, "charm") || strings.Contains(lower, "rollback") {
				t.Errorf("%s declares separately named rollback build %q", test.name, build.ID)
			}
		}

		for _, archive := range config.Archives {
			if !slices.Contains(test.wantArchives, archive.ID) {
				t.Errorf("%s declares unexpected alternate archive %q", test.name, archive.ID)
			}

			identity := strings.ToLower(archive.ID + " " + archive.NameTemplate)
			if strings.Contains(identity, "charm") || strings.Contains(identity, "rollback") {
				t.Errorf("%s declares separately named rollback archive %q (%q)", test.name, archive.ID, archive.NameTemplate)
			}
		}
	}
}
