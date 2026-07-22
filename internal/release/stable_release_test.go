package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

func loadStableReleaseWorkflow(t *testing.T) devReleaseWorkflow {
	t.Helper()

	data := mustReadReleaseFile(t, ".github/workflows/goreleaser.yml")

	var workflow devReleaseWorkflow

	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse stable release workflow: %v", err)
	}

	return workflow
}

func TestStableReleaseWorkflowBuildsBeforeOneControlledPublisher(t *testing.T) {
	workflow := loadStableReleaseWorkflow(t)
	for _, event := range []string{"push", "pull_request"} {
		if _, ok := workflow.Events[event]; !ok {
			t.Errorf("stable workflow has no %q validation path", event)
		}
	}

	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		t.Errorf("stable workflow top-level permissions = %#v, want contents:read", workflow.Permissions)
	}

	wantNeeds := map[string]workflowNeeds{
		"release-context": nil,
		"build-darwin":    {"release-context"},
		"build-linux":     {"release-context"},
		"execute-linux":   {"release-context", "build-linux"},
		"assemble-stable": {"release-context", "build-darwin", "build-linux", "execute-linux"},
		"attest-stable":   {"release-context", "assemble-stable"},
		"publish-stable":  {"release-context", "assemble-stable", "attest-stable"},
	}
	for name, want := range wantNeeds {
		job, ok := workflow.Jobs[name]
		if !ok {
			t.Errorf("stable workflow has no %q job", name)
			continue
		}

		if !slices.Equal(job.Needs, want) {
			t.Errorf("%s needs = %v, want %v", name, job.Needs, want)
		}
	}

	if len(workflow.Jobs) != len(wantNeeds) {
		t.Errorf("stable workflow jobs = %d, want controlled set %d", len(workflow.Jobs), len(wantNeeds))
	}

	if workflow.Jobs["publish-stable"].If != "github.event_name == 'push'" {
		t.Fatal("stable publisher is not restricted to real tag events")
	}
}

func TestStableReleaseWorkflowVerifiesFinalPackagesOnActualArchitectures(t *testing.T) {
	workflow := loadStableReleaseWorkflow(t)
	build := workflow.Jobs["build-linux"]
	execute := workflow.Jobs["execute-linux"]

	if build.RunsOn != "ubuntu-24.04" {
		t.Errorf("Linux materializer runner = %q", build.RunsOn)
	}

	if len(build.Strategy.Matrix.Include) != 2 || len(execute.Strategy.Matrix.Include) != 2 {
		t.Fatal("stable Linux build/execute matrices do not cover two architectures")
	}

	runners := make(map[string]string)

	for _, lane := range execute.Strategy.Matrix.Include {
		runners[lane["goarch"]] = lane["runner"]
	}

	if runners["amd64"] != "ubuntu-24.04" || runners["arm64"] != "ubuntu-24.04-arm" {
		t.Errorf("actual-architecture runners = %#v", runners)
	}

	buildVerification := workflowStep(build, "Verify archive and every package final byte").Run
	execution := workflowStep(execute, "Reverify and exercise release-shaped bytes").Run

	for _, script := range []string{buildVerification, execution} {
		for _, required := range []string{
			"verify-linux-release-bundle", ".deb", ".rpm", ".apk",
			"RELEASE_REVISION",
		} {
			if !strings.Contains(script, required) {
				t.Errorf("Linux final-byte validation missing %q", required)
			}
		}
	}

	if !strings.Contains(buildVerification, "GRAITH_SPDX_VALIDATOR_JAR") ||
		!strings.Contains(execution, "install-spdx-validator") {
		t.Fatal("Linux build and execution lanes do not independently validate SPDX")
	}

	for _, required := range []string{
		"TestLibghosttyDaemonLifecycle", "TestLibghosttyCharmToNativeUpgrade",
		"GRAITH_LIBGHOSTTY_UPGRADE_FROM_BINARY", "-race", "releaseartifact",
	} {
		if !strings.Contains(execution, required) {
			t.Errorf("actual-architecture lifecycle validation missing %q", required)
		}
	}
}

func TestStablePublisherStagesDraftAndPublishesOnlyCompleteSet(t *testing.T) {
	workflow := loadStableReleaseWorkflow(t)
	if strings.Contains(string(mustReadReleaseFile(t, ".github/workflows/goreleaser.yml")), "darwin_amd64") {
		t.Fatal("stable workflow still contains an unsupported Darwin amd64 path")
	}

	assemble := workflowStep(workflow.Jobs["assemble-stable"], "Verify same-revision manifests and exact output set").Run

	for _, required := range []string{
		"darwin_arm64.tar.gz",
		"linux_amd64.tar.gz", "linux_arm64.tar.gz",
		"linux_amd64.deb", "linux_amd64.rpm", "linux_amd64.apk",
		"linux_arm64.deb", "linux_arm64.rpm", "linux_arm64.apk",
		`test "$(wc -l <checksums.txt)" -eq 9`,
	} {
		if !strings.Contains(assemble, required) {
			t.Errorf("stable aggregation missing %q", required)
		}
	}

	if strings.Contains(assemble, "darwin_amd64") {
		t.Fatal("stable aggregation still accepts an unsupported Darwin amd64 artifact")
	}

	publisher := workflow.Jobs["publish-stable"]
	combined := make([]string, 0, len(publisher.Steps))

	for _, step := range publisher.Steps {
		combined = append(combined, step.Name+"\n"+step.Run)
	}

	text := strings.Join(combined, "\n")

	for _, required := range []string{
		`draft="$(jq -er '.draft'`, "unexpected=", "cmp \"$existing/$name\" \"dist/$name\"",
		"gh attestation verify", "--source-digest", "gh release upload",
		"render-stable-homebrew.sh", "publish-linux-repositories.sh",
		"render-stable-aur.sh", "gh release download", "--draft=false",
		"if [ \"$draft\" = false ]", "Publish prepared Homebrew formula",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("stable publisher missing %q", required)
		}
	}

	if strings.Contains(text, "gh release create") || strings.Contains(text, "--clobber") {
		t.Fatal("stable publisher can replace or create an uncontrolled release")
	}

	if strings.Index(text, "gh release upload") > strings.LastIndex(text, "gh release download") ||
		strings.LastIndex(text, "gh release download") > strings.LastIndex(text, "--draft=false") {
		t.Fatal("stable publisher does not stage, reverify, then expose the draft")
	}

	exposeAt := strings.LastIndex(text, "--draft=false")
	for _, mutation := range []string{"scripts/publish-push.sh", "git -C aur-package push"} {
		if strings.LastIndex(text, mutation) <= exposeAt {
			t.Errorf("downstream mutation %q can run before public release bytes exist", mutation)
		}
	}
}

func TestStableRPMBytesAreFinalBeforeChecksumAndAttestation(t *testing.T) {
	workflow := loadStableReleaseWorkflow(t)
	steps := workflow.Jobs["attest-stable"].Steps
	signAt, verifyAt, attestAt, uploadAt := -1, -1, -1, -1

	for index, step := range steps {
		switch {
		case step.Name == "Apply configured RPM signatures before checksums and provenance":
			signAt = index

			for _, required := range []string{"rpm --addsign", "sha256sum", "GPG_PRIVATE_KEY", "GPG_PASSPHRASE"} {
				if !strings.Contains(step.Run+strings.Join([]string{step.Env["GPG_PRIVATE_KEY"], step.Env["GPG_PASSPHRASE"]}, "\n"), required) {
					t.Errorf("RPM finalization missing %q", required)
				}
			}
		case step.Name == "Reverify finalized Linux package contents":
			verifyAt = index
		case strings.Contains(step.Uses, "actions/attest@"):
			attestAt = index
		case step.Name == "Upload final signed/checksummed stable release":
			uploadAt = index
		}
	}

	if signAt < 0 || verifyAt <= signAt || attestAt <= verifyAt || uploadAt <= attestAt {
		t.Fatalf("RPM finalization/verification/attestation/upload order = %d/%d/%d/%d", signAt, verifyAt, attestAt, uploadAt)
	}

	publishScript := string(mustReadReleaseFile(t, "scripts/publish-linux-repositories.sh"))
	if strings.Contains(publishScript, "rpm --addsign") || !strings.Contains(publishScript, "signatures OK") {
		t.Fatal("apt/yum publisher mutates finalized RPM bytes or does not require their signature")
	}
}

func TestStableLinuxArchiveAndPackagesCarryNativeEvidence(t *testing.T) {
	data := string(mustReadReleaseFile(t, ".goreleaser-linux.yaml"))
	for _, required := range []string{
		"package-linux", "libghostty-native.spdx.json",
		"THIRD_PARTY_NOTICES.libghostty.md", "/usr/share/doc/graith/",
		"formats:\n      - deb\n      - rpm\n      - apk",
		"env CGO_ENABLED=0 CC=cc go run", "-extldflags=-Wl,--strip-debug",
	} {
		if !strings.Contains(data, required) {
			t.Errorf("stable Linux packaging config missing %q", required)
		}
	}

	verifier := string(mustReadReleaseFile(t, "scripts/libghostty-native.sh"))
	for _, required := range []string{
		"verify_linux_release_bundle", "cmp \"$archive_root/gr\" \"$binary_path\"",
		"package payload is incomplete or outside its allowlist",
		"verify_candidate_privacy", "verify_candidate_spdx",
		"rpm_payload=\"$staging/rpm-payload.cpio\"",
		"cpio -it --quiet --absolute-filenames <\"$rpm_payload\"",
		"cpio -idm --quiet --no-absolute-filenames <\"$rpm_payload\"",
		"stable Linux rpm contains a non-canonical member",
	} {
		if !strings.Contains(verifier, required) {
			t.Errorf("stable Linux final-byte verifier missing %q", required)
		}
	}

	if strings.Contains(verifier, "rpm2cpio \"$rpm\" | cpio") {
		t.Fatal("stable Linux verifier trusts rpm2cpio's unsigned-package pipeline status")
	}
}

func TestReleasePleaseCreatesStableTagsAsDrafts(t *testing.T) {
	data := mustReadReleaseFile(t, ".release-please-config.json")

	var config struct {
		Packages map[string]struct {
			Draft            bool `json:"draft"`
			ForceTagCreation bool `json:"force-tag-creation"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}

	if !config.Packages["."].Draft {
		t.Fatal("release-please must create a draft before the stable publisher stages assets")
	}

	if !config.Packages["."].ForceTagCreation {
		t.Fatal("release-please drafts must still create the tag that triggers the stable workflow")
	}
}

func TestStableWorkflowDoesNotCoalesceDistinctTags(t *testing.T) {
	const perRefConcurrency = "group: stable-release-${{ github.event_name == 'push' && github.ref || github.event.pull_request.number }}"

	data := string(mustReadReleaseFile(t, ".github/workflows/goreleaser.yml"))
	if !strings.Contains(data, perRefConcurrency) {
		t.Fatal("stable workflow must keep distinct tag builds in distinct top-level concurrency groups")
	}

	if strings.Contains(data, "github.event_name == 'push' && 'publish'") {
		t.Fatal("stable workflow coalesces distinct tag builds before the serialized publisher")
	}

	if !strings.Contains(data, "group: stable-publisher\n      cancel-in-progress: false\n      queue: max") {
		t.Fatal("stable publisher must retain every distinct tag in its serialized queue")
	}

	var workflow struct {
		Jobs map[string]struct {
			Concurrency struct {
				Group            string `yaml:"group"`
				CancelInProgress bool   `yaml:"cancel-in-progress"`
				Queue            string `yaml:"queue"`
			} `yaml:"concurrency"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(data), &workflow); err != nil {
		t.Fatal(err)
	}

	publisher := workflow.Jobs["publish-stable"].Concurrency
	if publisher.Group != "stable-publisher" || publisher.CancelInProgress || publisher.Queue != "max" {
		t.Fatalf("stable publisher concurrency policy = %#v", publisher)
	}

	actionlintConfig := string(mustReadReleaseFile(t, ".github/actionlint.yaml"))
	if strings.Count(actionlintConfig, "ignore:") != 1 || strings.Count(actionlintConfig, "unexpected key \"queue\"") != 1 {
		t.Fatal("actionlint's queue-schema waiver must remain single and narrowly scoped")
	}

	if !strings.Contains(actionlintConfig, ".github/workflows/goreleaser.yml:") ||
		!strings.Contains(actionlintConfig, "https://github.com/rhysd/actionlint/pull/654") {
		t.Fatal("actionlint's queue-schema waiver must stay scoped and linked to its upstream removal condition")
	}
}

func TestStableRenderersRejectIncompleteChecksums(t *testing.T) {
	checksums := t.TempDir() + "/checksums.txt"
	if err := os.WriteFile(checksums, []byte(strings.Repeat("a", 64)+"  graith_0.70.0_linux_amd64.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, script := range []string{"render-stable-homebrew.sh", "render-stable-aur.sh"} {
		output := t.TempDir()
		if script == "render-stable-homebrew.sh" {
			output += "/graith.rb"
		}

		command := exec.Command(releaseRootPath("scripts", script), "0.70.0", checksums, output)
		if data, err := command.CombinedOutput(); err == nil || !strings.Contains(string(data), "expected exactly one SHA-256") {
			t.Errorf("%s accepted incomplete checksums: %v: %s", script, err, data)
		}
	}
}
