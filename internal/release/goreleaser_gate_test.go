// Package release holds regression tests for the release pipeline defined in
// .github/workflows/goreleaser.yml. The workflow's publish-repo gate is pure
// shell embedded in YAML, so we extract that shell and execute it here under
// every combination of the three required secrets to lock in its behaviour.
package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// requiredSecrets are the three secrets publish-repo consumes: GPG_PRIVATE_KEY
// and GPG_PASSPHRASE sign the rpm/apt packages + metadata, RELEASE_TOKEN checks
// out and pushes the cross-repo graith-repo Pages tree. The gate must map all
// three (issue #768).
var requiredSecrets = []string{"GPG_PRIVATE_KEY", "GPG_PASSPHRASE", "RELEASE_TOKEN"}

// goreleaserWorkflowPath returns the path to the repo-root goreleaser workflow,
// relative to this test file (internal/release).
func goreleaserWorkflowPath() string {
	return filepath.Join("..", "..", ".github", "workflows", "goreleaser.yml")
}

// gpgCheckStep locates the `gpg_check` step in the goreleaser job and returns
// its `run:` script plus the env map it declares. This is the gate that decides
// whether the apt/yum publish job runs (issue #768).
func gpgCheckStep(t *testing.T) (script string, env map[string]string) {
	t.Helper()

	data, err := os.ReadFile(goreleaserWorkflowPath())
	if err != nil {
		t.Fatalf("reading goreleaser.yml: %v", err)
	}

	var wf struct {
		Jobs struct {
			Goreleaser struct {
				Steps []struct {
					ID  string            `yaml:"id"`
					Run string            `yaml:"run"`
					Env map[string]string `yaml:"env"`
				} `yaml:"steps"`
			} `yaml:"goreleaser"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("parsing goreleaser.yml: %v", err)
	}

	for _, s := range wf.Jobs.Goreleaser.Steps {
		if s.ID == "gpg_check" {
			return s.Run, s.Env
		}
	}

	t.Fatal("could not find the gpg_check step in the goreleaser job")

	return "", nil
}

// runGate executes the gpg_check shell with the given secret values (empty
// string == unset) and returns the has_key output and whether the step failed
// (non-zero exit).
func runGate(t *testing.T, script string, secrets map[string]string) (hasKey string, failed bool) {
	t.Helper()

	outFile := filepath.Join(t.TempDir(), "github_output")
	if err := os.WriteFile(outFile, nil, 0o600); err != nil {
		t.Fatalf("seeding GITHUB_OUTPUT file: %v", err)
	}

	// Inherit the real environment (so bash and any external command resolve
	// via PATH), then layer GITHUB_OUTPUT and the three secrets on top.
	env := append(os.Environ(), "GITHUB_OUTPUT="+outFile)
	for _, name := range requiredSecrets {
		env = append(env, name+"="+secrets[name])
	}

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	failed = err != nil

	data, readErr := os.ReadFile(outFile)
	if readErr != nil {
		t.Fatalf("reading GITHUB_OUTPUT: %v", readErr)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "has_key="); ok {
			hasKey = v
		}
	}

	if hasKey == "" {
		t.Fatalf("gate wrote no has_key output; combined output:\n%s", out)
	}

	return hasKey, failed
}

// TestPublishGateMapsAllThreeSecrets guards against the gate regressing to
// keying only on GPG_PRIVATE_KEY. publish-repo also needs GPG_PASSPHRASE and
// RELEASE_TOKEN, so the step must map each one from the matching secret — not
// just declare an env var of the right name (issue #768).
func TestPublishGateMapsAllThreeSecrets(t *testing.T) {
	_, env := gpgCheckStep(t)

	for _, name := range requiredSecrets {
		want := "${{ secrets." + name + " }}"
		if got := env[name]; got != want {
			t.Errorf("gpg_check env[%s] = %q, want %q", name, got, want)
		}
	}
}

// TestPublishGateBehaviour exercises the extracted gate shell across all eight
// combinations of the three secrets. The bug in #768 was that a half-provisioned
// setup (key present, a companion secret missing) passed the gate and failed
// mid-publish. The gate must now publish only when all three are present, skip
// cleanly when GPG_PRIVATE_KEY is absent, and fail fast on partial provisioning.
func TestPublishGateBehaviour(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available; skipping gate execution test")
	}

	script, _ := gpgCheckStep(t)

	const set = "x"

	cases := []struct {
		name       string
		secrets    map[string]string
		wantHasKey string
		wantFailed bool
	}{
		{
			name:       "all three present publishes",
			secrets:    map[string]string{"GPG_PRIVATE_KEY": set, "GPG_PASSPHRASE": set, "RELEASE_TOKEN": set},
			wantHasKey: "true",
			wantFailed: false,
		},
		{
			name:       "nothing configured skips cleanly",
			secrets:    map[string]string{},
			wantHasKey: "false",
			wantFailed: false,
		},
		{
			name:       "passphrase only skips cleanly",
			secrets:    map[string]string{"GPG_PASSPHRASE": set},
			wantHasKey: "false",
			wantFailed: false,
		},
		{
			name:       "release token only skips cleanly",
			secrets:    map[string]string{"RELEASE_TOKEN": set},
			wantHasKey: "false",
			wantFailed: false,
		},
		{
			name:       "passphrase and token but no key skips cleanly",
			secrets:    map[string]string{"GPG_PASSPHRASE": set, "RELEASE_TOKEN": set},
			wantHasKey: "false",
			wantFailed: false,
		},
		{
			name:       "key alone fails fast",
			secrets:    map[string]string{"GPG_PRIVATE_KEY": set},
			wantHasKey: "false",
			wantFailed: true,
		},
		{
			name:       "key without passphrase fails fast",
			secrets:    map[string]string{"GPG_PRIVATE_KEY": set, "RELEASE_TOKEN": set},
			wantHasKey: "false",
			wantFailed: true,
		},
		{
			name:       "key without release token fails fast",
			secrets:    map[string]string{"GPG_PRIVATE_KEY": set, "GPG_PASSPHRASE": set},
			wantHasKey: "false",
			wantFailed: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hasKey, failed := runGate(t, script, tc.secrets)

			if hasKey != tc.wantHasKey {
				t.Errorf("has_key = %q, want %q", hasKey, tc.wantHasKey)
			}

			if failed != tc.wantFailed {
				t.Errorf("step failed = %v, want %v", failed, tc.wantFailed)
			}
		})
	}
}
