package cibaseline

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCommittedInventoryMatchesRepository(t *testing.T) {
	repo := filepath.Join("..", "..")

	got, err := BuildInventory(repo)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile("inventory.json")
	if err != nil {
		t.Fatal(err)
	}

	var want Inventory
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}

	if got.Digest != want.Digest {
		t.Fatalf("inventory digest = %s, want %s; run go run ./cmd/cibaseline -output internal/cibaseline/inventory.json generate", want.Digest, got.Digest)
	}

	second, err := BuildInventory(repo)
	if err != nil {
		t.Fatal(err)
	}

	if got.Digest != second.Digest {
		t.Fatalf("non-deterministic digest: %s != %s", got.Digest, second.Digest)
	}
}

func TestInventoryValidationFailures(t *testing.T) {
	base := loadInventory(t)

	tests := []struct {
		name string
		edit func(*Inventory)
		want string
	}{
		{"missing workflow", func(i *Inventory) { i.Workflows = i.Workflows[1:] }, "missing workflows"},
		{"extra workflow", func(i *Inventory) { i.Workflows[0].ID = "dreich" }, "extra workflow"},
		{"missing owner", func(i *Inventory) { i.Workflows[0].Owner = "" }, "has no owner"},
		{"missing CI configuration surface", func(i *Inventory) {
			for index, surface := range i.Surfaces {
				if surface.Path == ciConfigurationPaths[0] {
					i.Surfaces = append(i.Surfaces[:index], i.Surfaces[index+1:]...)

					return
				}
			}
		}, "missing CI configuration surface"},
		{"missing CI entrypoint surface", func(i *Inventory) {
			for index, surface := range i.Surfaces {
				if surface.Path == ciEntrypointPaths[0] {
					i.Surfaces = append(i.Surfaces[:index], i.Surfaces[index+1:]...)

					return
				}
			}
		}, "missing CI entrypoint surface"},
		{"duplicate coordinate", func(i *Inventory) {
			i.Workflows[0].Jobs[1].Coordinates[0] = i.Workflows[0].Jobs[0].Coordinates[0]
		}, "duplicate job coordinate"},
		{"missing mapping", func(i *Inventory) { i.Mappings = i.Mappings[1:] }, "missing mapping"},
		{"duplicate mapping", func(i *Inventory) { i.Mappings = append(i.Mappings, i.Mappings[0]) }, "duplicate mapping"},
		{"orphan mapping", func(i *Inventory) {
			i.Mappings[0].LegacyCoordinate = "croft/bothy"
		}, "orphan mapping"},
		{"unjustified new obligation", func(i *Inventory) {
			i.Mappings[0].NewObligation = true
			i.Mappings[0].Justification = ""
		}, "unjustified"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inventory := cloneInventory(t, base)
			test.edit(&inventory)
			resign(t, &inventory)

			if err := inventory.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCurrentValidationRejectsMissingAndExtraJobRows(t *testing.T) {
	repo := filepath.Join("..", "..")
	base := loadInventory(t)

	tests := []struct {
		name string
		edit func(*Inventory)
	}{
		{"missing job", func(i *Inventory) {
			i.Workflows[0].Jobs = i.Workflows[0].Jobs[1:]
			i.Mappings = i.Mappings[1:]
		}},
		{"extra job", func(i *Inventory) {
			job := i.Workflows[0].Jobs[0]
			job.ID, job.Name, job.Coordinates, job.GitHubNames = "dreich", "Dreich", []string{"ci/dreich"}, []string{"Dreich"}
			i.Workflows[0].Jobs = append(i.Workflows[0].Jobs, job)
			i.Mappings = append(i.Mappings, Mapping{
				LegacyCoordinate: "ci/dreich", NewMode: "legacy/ci/dreich", Owner: job.Owner,
				LegacyCondition: job.Condition, SkipSemantics: job.SkipSemantics,
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inventory := cloneInventory(t, base)
			test.edit(&inventory)
			resign(t, &inventory)

			if err := ValidateCurrent(repo, inventory); err == nil || !strings.Contains(err.Error(), "inventory is stale") {
				t.Fatalf("ValidateCurrent() error = %v, want stale inventory", err)
			}
		})
	}
}

func TestWorkflowFileSetRejectsRepositoryAddition(t *testing.T) {
	repo := t.TempDir()

	root := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}

	for _, id := range workflowIDs {
		if err := os.WriteFile(filepath.Join(root, id+".yml"), []byte("name: braw\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := validateWorkflowFiles(repo); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "dreich.yaml"), []byte("name: dreich\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateWorkflowFiles(repo); err == nil || !strings.Contains(err.Error(), "extra=[dreich.yaml]") {
		t.Fatalf("validateWorkflowFiles() error = %v, want extra workflow rejection", err)
	}
}

func TestInventoryCarriesReviewedCapabilitiesAndPolicySurfaces(t *testing.T) {
	inventory := loadInventory(t)

	var foundAttestation bool

	for _, workflow := range inventory.Workflows {
		for _, job := range workflow.Jobs {
			if job.Capability == "" {
				t.Fatalf("job %s/%s has no capability", workflow.ID, job.ID)
			}

			if workflow.ID == "dev-release" && job.ID == "attest-linux" {
				foundAttestation = job.ProofType == "package-consumer"
			}
		}
	}

	surfaces := map[string]bool{}
	for _, surface := range inventory.Surfaces {
		surfaces[surface.Path] = true
	}

	if !foundAttestation {
		t.Fatal("explicit attestation proof type is missing")
	}

	paths := append([]string{".github/workflows/scripts/package-lock.json"}, ciConfigurationPaths...)

	paths = append(paths, ciEntrypointPaths...)
	for _, path := range paths {
		if !surfaces[path] {
			t.Errorf("policy surface %q is missing", path)
		}
	}

	for _, surface := range inventory.Surfaces {
		if surface.Path == "gui/ios/Makefile" && surface.Owner != "gui-owners" {
			t.Errorf("gui/ios/Makefile owner = %q, want gui-owners", surface.Owner)
		}

		if surface.Path == "libghostty-native.lock.json" &&
			(surface.Owner != "native-owners" || !strings.Contains(surface.Contract, "SHA-256")) {
			t.Errorf("native lock surface identity = %#v, want owned supply-chain contract", surface)
		}
	}
}

func TestInventorySurfacesIncludeOnlyTrackedRepositoryInputs(t *testing.T) {
	repo := t.TempDir()

	tracked := append([]string{}, ciConfigurationPaths...)
	tracked = append(tracked, ciEntrypointPaths...)

	tracked = append(tracked, ".github/workflows/scripts/braw.js", "scripts/canny.sh")
	for _, path := range tracked {
		fullPath := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(fullPath, []byte("dreich\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	runGit(t, repo, "init")
	runGit(t, repo, "add", "--", ".")

	untracked := []string{
		".github/workflows/scripts/blether.js",
		".github/workflows/scripts/node_modules/croft/index.js",
	}
	for _, path := range untracked {
		fullPath := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(fullPath, []byte("bothy\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	surfaces, err := inventorySurfaces(repo)
	if err != nil {
		t.Fatal(err)
	}

	paths := map[string]bool{}
	for _, surface := range surfaces {
		paths[surface.Path] = true
	}

	for _, path := range append(ciEntrypointPaths, ".github/workflows/scripts/braw.js", "scripts/canny.sh") {
		if !paths[path] {
			t.Errorf("tracked surface %q is missing", path)
		}
	}

	for _, path := range untracked {
		if paths[path] {
			t.Errorf("untracked surface %q was inventoried", path)
		}
	}
}

func TestInventorySurfacesIgnoreReleasePleaseManifestContent(t *testing.T) {
	repo := t.TempDir()

	tracked := append([]string{}, ciConfigurationPaths...)
	tracked = append(tracked, ciEntrypointPaths...)
	tracked = append(tracked, ".release-please-manifest.json")

	for _, path := range tracked {
		fullPath := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
			t.Fatal(err)
		}

		content := "braw\n"
		if path == ".release-please-manifest.json" {
			content = `{".":"braw"}` + "\n"
		}

		if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	runGit(t, repo, "init")
	runGit(t, repo, "add", "--", ".")

	before, err := inventorySurfaces(repo)
	if err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(repo, ".release-please-manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{".":"canny"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	after, err := inventorySurfaces(repo)
	if err != nil {
		t.Fatal(err)
	}

	for _, surface := range after {
		if surface.Path == ".release-please-manifest.json" {
			t.Fatal("release-please manifest was inventoried as a CI policy surface")
		}
	}

	beforeData, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}

	afterData, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}

	if string(beforeData) != string(afterData) {
		t.Fatalf("release-please manifest content changed inventory surfaces:\nbefore=%s\nafter=%s", beforeData, afterData)
	}
}

func TestInventorySurfaceIndexedModeParticipatesInIdentity(t *testing.T) {
	repo := t.TempDir()

	tracked := append([]string{}, ciConfigurationPaths...)
	tracked = append(tracked, ciEntrypointPaths...)
	tracked = append(tracked, "scripts/canny.sh")

	for _, path := range tracked {
		fullPath := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(fullPath, []byte("braw\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	runGit(t, repo, "init")
	runGit(t, repo, "add", "--", ".")

	before, err := inventorySurfaces(repo)
	if err != nil {
		t.Fatal(err)
	}

	runGit(t, repo, "update-index", "--chmod=+x", "--", "scripts/canny.sh")

	after, err := inventorySurfaces(repo)
	if err != nil {
		t.Fatal(err)
	}

	var beforeMode, afterMode string

	for _, surface := range before {
		if surface.Path == "scripts/canny.sh" {
			beforeMode = surface.GitMode
		}
	}

	for _, surface := range after {
		if surface.Path == "scripts/canny.sh" {
			afterMode = surface.GitMode
		}
	}

	if beforeMode != "100644" || afterMode != "100755" {
		t.Fatalf("indexed modes = %q -> %q, want 100644 -> 100755", beforeMode, afterMode)
	}

	inventory := loadInventory(t)
	inventory.Surfaces[0].GitMode = "100755"
	resign(t, &inventory)

	if err := ValidateCurrent(filepath.Join("..", ".."), inventory); err == nil ||
		!strings.Contains(err.Error(), "inventory is stale") {
		t.Fatalf("ValidateCurrent(chmod-only inventory) error = %v, want stale inventory", err)
	}
}

func TestInventorySurfaceRejectsNonRegularIndexedType(t *testing.T) {
	repo := t.TempDir()

	tracked := append([]string{}, ciConfigurationPaths...)
	tracked = append(tracked, ciEntrypointPaths...)

	for _, path := range tracked {
		fullPath := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(fullPath, []byte("braw\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("../Makefile", filepath.Join(repo, "scripts", "bothy")); err != nil {
		t.Fatal(err)
	}

	runGit(t, repo, "init")
	runGit(t, repo, "add", "--", ".")

	if _, err := inventorySurfaces(repo); err == nil ||
		!strings.Contains(err.Error(), "unsupported indexed mode 120000") {
		t.Fatalf("inventorySurfaces(symlink) error = %v, want non-regular rejection", err)
	}
}

func TestInventorySurfaceRejectsConflictedIndex(t *testing.T) {
	repo := t.TempDir()

	tracked := append([]string{}, ciConfigurationPaths...)
	tracked = append(tracked, ciEntrypointPaths...)
	tracked = append(tracked, "scripts/canny.sh")

	for _, path := range tracked {
		fullPath := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(fullPath, []byte("braw\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	runGit(t, repo, "init")
	runGit(t, repo, "add", "--", ".")

	output, err := exec.Command("git", "-C", repo, "ls-files", "--stage", "--", "scripts/canny.sh").Output()
	if err != nil {
		t.Fatal(err)
	}

	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		t.Fatalf("unexpected index entry %q", output)
	}

	runGit(t, repo, "update-index", "--force-remove", "--", "scripts/canny.sh")
	command := exec.Command("git", "-C", repo, "update-index", "--index-info")

	command.Stdin = strings.NewReader(
		"100644 " + fields[1] + " 1\tscripts/canny.sh\n" +
			"100644 " + fields[1] + " 2\tscripts/canny.sh\n",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create conflicted index: %v\n%s", err, output)
	}

	if _, err := inventorySurfaces(repo); err == nil ||
		!strings.Contains(err.Error(), "non-stage-zero index entry") {
		t.Fatalf("inventorySurfaces(conflict) error = %v, want conflict rejection", err)
	}
}

func TestMatrixDefaultNamePreservesDeclarationOrder(t *testing.T) {
	var raw jobYAML
	if err := yaml.Unmarshal([]byte(`
strategy:
  matrix:
    include:
      - target: x86_64-linux-gnu
        goarch: amd64
`), &raw); err != nil {
		t.Fatal(err)
	}

	matrix, err := matrixFromStrategy(raw.Strategy)
	if err != nil {
		t.Fatal(err)
	}

	_, names, err := expandCoordinates("libghostty-native-publish", "publish", "publish", matrix)
	if err != nil {
		t.Fatal(err)
	}

	if len(names) != 1 || names[0] != "publish (x86_64-linux-gnu, amd64)" {
		t.Fatalf("GitHub matrix names = %v, want declaration order", names)
	}
}

func TestUnsupportedMatrixSemanticsFailClosed(t *testing.T) {
	tests := map[string]string{
		"exclude": `
strategy:
  matrix:
    goarch: [amd64, arm64]
    exclude:
      - goarch: arm64
`,
		"include with axes": `
strategy:
  matrix:
    goarch: [amd64]
    include:
      - goarch: arm64
`,
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			var raw jobYAML
			if err := yaml.Unmarshal([]byte(input), &raw); err != nil {
				t.Fatal(err)
			}

			matrix, err := matrixFromStrategy(raw.Strategy)
			if err != nil {
				t.Fatal(err)
			}

			if _, _, err := expandCoordinates("ci", "braw", "braw", matrix); err == nil {
				t.Fatal("expandCoordinates() accepted unsupported matrix semantics")
			}
		})
	}
}

func TestUnsupportedReusableWorkflowAndStaleProofClassificationFailClosed(t *testing.T) {
	var raw jobYAML
	if err := yaml.Unmarshal([]byte(`uses: d0ugal/canny/.github/workflows/braw.yml@main`), &raw); err != nil {
		t.Fatal(err)
	}

	if _, err := inventoryJob("ci", "blether", raw); err == nil ||
		!strings.Contains(err.Error(), "reusable workflow") {
		t.Fatalf("inventoryJob(reusable workflow) error = %v, want fail-closed rejection", err)
	}

	inventory := loadInventory(t)

	classifications := make(map[string]string, len(proofTypes)+1)
	for coordinate, proofType := range proofTypes {
		classifications[coordinate] = proofType
	}

	classifications["ci/dreich"] = "runtime"

	if err := validateProofTypeSet(inventory.Workflows, classifications); err == nil ||
		!strings.Contains(err.Error(), "stale proof classification") {
		t.Fatalf("validateProofTypeSet(stale row) error = %v, want rejection", err)
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()

	commandArgs := append([]string{"-C", repo}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func loadInventory(t *testing.T) Inventory {
	t.Helper()

	data, err := os.ReadFile("inventory.json")
	if err != nil {
		t.Fatal(err)
	}

	var inventory Inventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}

	return inventory
}

func cloneInventory(t *testing.T, inventory Inventory) Inventory {
	t.Helper()

	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}

	var clone Inventory
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}

	return clone
}

func resign(t *testing.T, inventory *Inventory) {
	t.Helper()

	if err := inventory.setDigest(); err != nil {
		t.Fatal(err)
	}
}
