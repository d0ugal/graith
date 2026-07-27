// Package cibaseline inventories the current GitHub Actions proof surface for
// the CI north-star static baseline.
package cibaseline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

const SchemaVersion = 2

var workflowIDs = []string{
	"ci", "coverage", "gui-ci", "libghostty-native", "libghostty-native-publish",
	"regen", "docs-preview", "dev-release", "release-please", "goreleaser",
	"sandbox", "dependency-review", "codeql", "scorecard", "secret-scan",
	"workflow-lint", "docs", "commits",
}

var requiredContexts = map[string]bool{
	"Test (macos-latest)": true, "Lint": true, "Conventional commits": true,
	"Test (ubuntu-latest)": true, "macOS (safehouse / Seatbelt)": true,
	"Linux (nono / Landlock)": true, "Native backend gate": true,
}

var ciConfigurationPaths = []string{
	".github/actionlint.yaml",
	".golangci.yml",
	".goreleaser-dev.yaml",
	".goreleaser-linux.yaml",
	".goreleaser.yaml",
	// .release-please-manifest.json is release state rewritten on each release PR.
	// Keep Release Please policy pinned through .release-please-config.json.
	".release-please-config.json",
	"libghostty-native.lock.json",
	"renovate.json5",
}

var ciEntrypointPaths = []string{
	"Makefile",
	"gui/ios/Makefile",
}

var ciGoPolicySurfacePaths = []string{
	"cmd/docsdiff/main.go",
	"cmd/docsdiff/main_test.go",
	"cmd/cipolicy/main.go",
	"cmd/cipolicy/main_test.go",
	"cmd/libghosttyarchive/main.go",
	"cmd/libghosttyarchive/main_test.go",
	"internal/cipolicy/io.go",
	"internal/cipolicy/libghostty_policy_test.go",
	"internal/cipolicy/p11_js_surface.go",
	"internal/cipolicy/p11_js_surface_test.go",
	"internal/cipolicy/renovate_retry_test.go",
	"internal/cipolicy/shadow_summary.go",
	"internal/cipolicy/shadow_summary_test.go",
	"internal/cipolicy/workflow_lint_policy_test.go",
	"internal/libghosttyarchive/archive.go",
	"internal/libghosttyarchive/archive_test.go",
}

type goPolicySurfaceMetadata struct {
	kind       string
	contract   string
	retirement string
}

var ciGoPolicySurfaces = map[string]goPolicySurfaceMetadata{
	"cmd/docsdiff/main.go": {
		kind:       "go-policy-helper",
		contract:   "preserve docs-preview screenshot diff classification, row alignment, denoise, composite PNG rendering, and manifest output behavior formerly held by docs-diff.js and docs-diff-run.js",
		retirement: "remove only with the docs-preview screenshot-diff workflow caller or an owner-approved replacement helper",
	},
	"cmd/docsdiff/main_test.go": {
		kind:       "go-policy-contract-test",
		contract:   "preserve parity coverage for docs-preview screenshot diff row hashing, insert/delete alignment, denoise, hunk padding and merging, manifest ordering, page classifications, exit behavior, and composite PNG geometry",
		retirement: "remove only after equivalent docs-preview screenshot-diff parity coverage exists",
	},
	"cmd/cipolicy/main.go": {
		kind:       "go-policy-helper",
		contract:   "preserve the checked-in cipolicy CLI behavior invoked by workflow policy detection and the CI shadow summary",
		retirement: "remove only with the cipolicy workflow caller or an owner-approved replacement command",
	},
	"cmd/cipolicy/main_test.go": {
		kind:       "go-policy-contract-test",
		contract:   "preserve CLI coverage for policy plan and diagnostic summary command behavior",
		retirement: "remove only after equivalent cipolicy command coverage is added",
	},
	"cmd/libghosttyarchive/main.go": {
		kind:       "go-policy-helper",
		contract:   "preserve libghostty Linux archive helper pack, inspect, test, diagnostics, and exit-code behavior invoked by native producer and consumer workflows",
		retirement: "remove only with equivalent native producer and consumer archive validation owned by the repository",
	},
	"cmd/libghosttyarchive/main_test.go": {
		kind:       "go-policy-contract-test",
		contract:   "preserve libghostty archive helper CLI contract coverage for successful operation, usage errors, validation failures, and self-test behavior",
		retirement: "remove only after equivalent command contract coverage is added",
	},
	"internal/cipolicy/io.go": {
		kind:       "go-policy-helper",
		contract:   "preserve strict JSON decoding for inventory, manifest, and run-plan inputs consumed by policy tooling",
		retirement: "remove only with equivalent checked-in structured input decoding",
	},
	"internal/cipolicy/libghostty_policy_test.go": {
		kind:       "go-policy-contract-test",
		contract:   "preserve native/release/coverage routing and libghostty artifact trust assertions formerly held by libghostty-policy.test.js",
		retirement: "owned replacement has equivalent native policy coverage and zero unexplained replay disagreement",
	},
	"internal/cipolicy/p11_js_surface.go": {
		kind:       "go-policy-contract",
		contract:   "preserve current retained-JS surface contracts and semantic replacement metadata for retired workflow-script tests",
		retirement: "remove only after all P11 JS surfaces are retired or represented by a newer owner-approved contract",
	},
	"internal/cipolicy/p11_js_surface_test.go": {
		kind:       "go-policy-contract-test",
		contract:   "preserve regen credential, trust, scalar sweep, git-index enumeration, and repository-command detection assertions formerly held by regen-auth.test.js",
		retirement: "owned replacement has equivalent regen-auth coverage and zero unexplained replay disagreement",
	},
	"internal/cipolicy/renovate_retry_test.go": {
		kind:       "go-policy-contract-test",
		contract:   "preserve bounded transient Renovate retry behavior formerly held by renovate-retry.test.js",
		retirement: "owned replacement has equivalent retry fixture coverage and zero unexplained replay disagreement",
	},
	"internal/cipolicy/shadow_summary.go": {
		kind:       "go-policy-helper",
		contract:   "preserve non-authoritative CI shadow summary rendering, required-context inventory listing, native coverage note, and no-live-aggregation boundary",
		retirement: "delete with the CI shadow summary job or replace only after equivalent diagnostic summary tests cover the same authority boundary",
	},
	"internal/cipolicy/shadow_summary_test.go": {
		kind:       "go-policy-contract-test",
		contract:   "preserve diagnostic wording, detector fallback, helper language, and no-live-aggregation assertions for the CI shadow summary",
		retirement: "delete with the CI shadow summary helper or replace only after equivalent summary contract coverage exists",
	},
	"internal/cipolicy/workflow_lint_policy_test.go": {
		kind:       "go-policy-contract-test",
		contract:   "preserve ShellCheck coverage and provenance-verified workflow-lint install assertions formerly held by shellcheck-policy.test.js and workflow-lint-supply-chain.test.js",
		retirement: "owned replacement has equivalent workflow-lint policy coverage and zero unexplained replay disagreement",
	},
	"internal/libghosttyarchive/archive.go": {
		kind:       "go-policy-helper",
		contract:   "preserve deterministic libghostty Linux archive construction, metadata normalization, member validation, and malformed archive rejection",
		retirement: "remove only with equivalent archive construction and validation coverage owned by the repository",
	},
	"internal/libghosttyarchive/archive_test.go": {
		kind:       "go-policy-contract-test",
		contract:   "preserve old-vs-new parity, deterministic tar metadata, traversal rejection, malformed input rejection, and pack validation coverage",
		retirement: "remove only after equivalent archive parity and failure-mode coverage exists",
	},
}

type Inventory struct {
	SchemaVersion              int        `json:"schema_version"`
	ObservationState           string     `json:"observation_state"`
	Workflows                  []Workflow `json:"workflows"`
	Surfaces                   []Surface  `json:"policy_surfaces"`
	Mappings                   []Mapping  `json:"legacy_mappings"`
	RequiredContexts           []string   `json:"required_contexts"`
	RequiredContextsSource     string     `json:"required_contexts_source"`
	RequiredContextsObservedAt string     `json:"required_contexts_observed_at"`
	Digest                     string     `json:"digest"`
}

type Workflow struct {
	ID          string          `json:"id"`
	Path        string          `json:"path"`
	Name        string          `json:"name"`
	Owner       string          `json:"owner"`
	FileSHA256  string          `json:"file_sha256"`
	Events      json.RawMessage `json:"events"`
	Permissions json.RawMessage `json:"permissions"`
	Environment json.RawMessage `json:"environment"`
	Jobs        []Job           `json:"jobs"`
}

type Job struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Capability      string          `json:"capability"`
	Owner           string          `json:"owner"`
	Runner          json.RawMessage `json:"runner"`
	Condition       string          `json:"condition,omitempty"`
	SkipSemantics   string          `json:"skip_semantics"`
	ProofType       string          `json:"proof_type"`
	Requiredness    string          `json:"requiredness"`
	Permissions     json.RawMessage `json:"permissions"`
	Environment     json.RawMessage `json:"environment"`
	Matrix          json.RawMessage `json:"matrix,omitempty"`
	Coordinates     []string        `json:"coordinates"`
	GitHubNames     []string        `json:"github_names"`
	ActionPins      []string        `json:"action_pins,omitempty"`
	Toolchains      []Observation   `json:"toolchains,omitempty"`
	CacheActions    []Observation   `json:"cache_actions,omitempty"`
	ArtifactActions []Observation   `json:"artifact_actions,omitempty"`
}

type Observation struct {
	Action string          `json:"action"`
	Name   json.RawMessage `json:"name,omitempty"`
	With   json.RawMessage `json:"with,omitempty"`
}

type Surface struct {
	Path        string `json:"path"`
	Owner       string `json:"owner"`
	Kind        string `json:"kind"`
	GitMode     string `json:"git_mode"`
	SHA256      string `json:"sha256"`
	Contract    string `json:"executable_contract"`
	Disposition string `json:"disposition"`
	Retirement  string `json:"retirement_criterion"`
}

type Mapping struct {
	LegacyCoordinate string `json:"legacy_coordinate"`
	LegacyCondition  string `json:"legacy_condition,omitempty"`
	SkipSemantics    string `json:"skip_semantics"`
	NewMode          string `json:"new_mode,omitempty"`
	Retirement       string `json:"retirement,omitempty"`
	Owner            string `json:"owner"`
	NewObligation    bool   `json:"new_obligation"`
	Justification    string `json:"justification,omitempty"`
}

type workflowYAML struct {
	Name        string             `yaml:"name"`
	On          any                `yaml:"on"`
	Permissions any                `yaml:"permissions"`
	Env         any                `yaml:"env"`
	Jobs        map[string]jobYAML `yaml:"jobs"`
}

type jobYAML struct {
	Name        string           `yaml:"name"`
	RunsOn      any              `yaml:"runs-on"`
	Uses        string           `yaml:"uses"`
	If          string           `yaml:"if"`
	Permissions any              `yaml:"permissions"`
	Env         any              `yaml:"env"`
	Strategy    yaml.Node        `yaml:"strategy"`
	Steps       []map[string]any `yaml:"steps"`
}

type matrixModel struct {
	value       any
	keys        []string
	includeRows []matrixRow
	hasInclude  bool
	hasExclude  bool
}

type matrixRow struct {
	values map[string]any
	order  []string
}

func BuildInventory(repo string) (Inventory, error) {
	inv := Inventory{
		SchemaVersion: SchemaVersion, ObservationState: "p0-observation-in-progress",
		RequiredContextsSource:     "reviewed GitHub branch protection API snapshot for refs/heads/main",
		RequiredContextsObservedAt: "2026-07-25T16:54:00Z",
	}

	if err := validateWorkflowFiles(repo); err != nil {
		return Inventory{}, err
	}

	for _, id := range workflowIDs {
		path := filepath.Join(".github", "workflows", id+".yml")

		data, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			return Inventory{}, fmt.Errorf("read workflow %s: %w", id, err)
		}

		var raw workflowYAML
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return Inventory{}, fmt.Errorf("parse workflow %s: %w", id, err)
		}

		w := Workflow{ID: id, Path: filepath.ToSlash(path), Name: raw.Name, Owner: workflowOwner(id), FileSHA256: sum(data)}

		w.Events, err = canonical(raw.On)
		if err != nil {
			return Inventory{}, err
		}

		w.Permissions, err = canonical(raw.Permissions)
		if err != nil {
			return Inventory{}, err
		}

		w.Environment, err = canonical(raw.Env)
		if err != nil {
			return Inventory{}, err
		}

		ids := make([]string, 0, len(raw.Jobs))
		for jobID := range raw.Jobs {
			ids = append(ids, jobID)
		}

		sort.Strings(ids)

		for _, jobID := range ids {
			job, err := inventoryJob(id, jobID, raw.Jobs[jobID])
			if err != nil {
				return Inventory{}, err
			}

			w.Jobs = append(w.Jobs, job)
			for _, coordinate := range job.Coordinates {
				inv.Mappings = append(inv.Mappings, Mapping{
					LegacyCoordinate: coordinate, NewMode: "legacy/" + coordinate,
					LegacyCondition: job.Condition, SkipSemantics: job.SkipSemantics, Owner: job.Owner,
				})
			}
		}

		inv.Workflows = append(inv.Workflows, w)
	}

	if err := validateProofTypeSet(inv.Workflows, proofTypes); err != nil {
		return Inventory{}, err
	}

	surfaces, err := inventorySurfaces(repo)
	if err != nil {
		return Inventory{}, err
	}

	if err := requireNamedSurfaces(surfaces, ciGoPolicySurfacePaths, "CI Go policy surface"); err != nil {
		return Inventory{}, err
	}

	inv.Surfaces = surfaces
	for context := range requiredContexts {
		inv.RequiredContexts = append(inv.RequiredContexts, context)
	}

	sort.Strings(inv.RequiredContexts)

	if err := inv.setDigest(); err != nil {
		return Inventory{}, err
	}

	return inv, inv.Validate()
}

func ValidateCurrent(repo string, committed Inventory) error {
	if err := committed.Validate(); err != nil {
		return err
	}

	current, err := BuildInventory(repo)
	if err != nil {
		return err
	}

	if committed.Digest != current.Digest {
		return fmt.Errorf("inventory is stale: got %s want %s", committed.Digest, current.Digest)
	}

	return nil
}

func inventoryJob(workflowID, id string, raw jobYAML) (Job, error) {
	if strings.TrimSpace(raw.Uses) != "" {
		return Job{}, fmt.Errorf("job %s/%s uses a reusable workflow, which P0 inventory does not support", workflowID, id)
	}

	name := raw.Name
	if name == "" {
		name = id
	}

	j := Job{
		ID: id, Name: name, Capability: "legacy-proof/" + workflowID + "/" + id,
		Owner: workflowOwner(workflowID), Condition: raw.If,
	}

	var err error
	if j.Runner, err = canonical(raw.RunsOn); err != nil {
		return Job{}, err
	}

	if j.Permissions, err = canonical(raw.Permissions); err != nil {
		return Job{}, err
	}

	if j.Environment, err = canonical(raw.Env); err != nil {
		return Job{}, err
	}

	matrix, err := matrixFromStrategy(raw.Strategy)
	if err != nil {
		return Job{}, fmt.Errorf("inventory %s/%s matrix: %w", workflowID, id, err)
	}

	if matrix.value != nil {
		j.Matrix, err = canonical(matrix.value)
		if err != nil {
			return Job{}, err
		}
	}

	j.SkipSemantics = "github-skipped-is-distinct-from-passed"

	var ok bool

	j.ProofType, ok = proofTypes[workflowID+"/"+id]
	if !ok {
		return Job{}, fmt.Errorf("job %s/%s has no explicit proof classification", workflowID, id)
	}

	if requiredContexts[name] {
		j.Requiredness = "required"
	} else {
		j.Requiredness = "soft"
	}

	j.Coordinates, j.GitHubNames, err = expandCoordinates(workflowID, id, name, matrix)
	if err != nil {
		return Job{}, fmt.Errorf("inventory %s/%s matrix: %w", workflowID, id, err)
	}

	for _, step := range raw.Steps {
		uses, _ := step["uses"].(string)
		if uses == "" {
			continue
		}

		j.ActionPins = append(j.ActionPins, uses)
		ob := Observation{Action: uses}

		ob.With, _ = canonical(step["with"])
		if with, ok := step["with"].(map[string]any); ok {
			ob.Name, _ = canonical(with["name"])
		}

		lower := strings.ToLower(uses)
		if strings.Contains(lower, "setup-") {
			j.Toolchains = append(j.Toolchains, ob)
		}

		if strings.Contains(lower, "cache") || strings.Contains(lower, "setup-go") || strings.Contains(lower, "setup-node") {
			j.CacheActions = append(j.CacheActions, ob)
		}

		if strings.Contains(lower, "artifact") {
			j.ArtifactActions = append(j.ArtifactActions, ob)
		}
	}

	sort.Strings(j.ActionPins)

	return j, nil
}

func validateProofTypeSet(workflows []Workflow, classifications map[string]string) error {
	current := make(map[string]bool)

	for _, workflow := range workflows {
		for _, job := range workflow.Jobs {
			current[workflow.ID+"/"+job.ID] = true
		}
	}

	for coordinate := range classifications {
		if !current[coordinate] {
			return fmt.Errorf("stale proof classification %s", coordinate)
		}
	}

	for coordinate := range current {
		if _, exists := classifications[coordinate]; !exists {
			return fmt.Errorf("missing proof classification %s", coordinate)
		}
	}

	return nil
}

func matrixFromStrategy(strategy yaml.Node) (matrixModel, error) {
	if strategy.Kind == 0 {
		return matrixModel{}, nil
	}

	if strategy.Kind != yaml.MappingNode {
		return matrixModel{}, errors.New("strategy must be a mapping")
	}

	var matrixNode *yaml.Node

	for index := 0; index < len(strategy.Content); index += 2 {
		if strategy.Content[index].Value == "matrix" {
			matrixNode = strategy.Content[index+1]

			break
		}
	}

	if matrixNode == nil {
		return matrixModel{}, nil
	}

	var value any
	if err := matrixNode.Decode(&value); err != nil {
		return matrixModel{}, err
	}

	model := matrixModel{value: value}

	if matrixNode.Kind != yaml.MappingNode {
		return matrixModel{}, errors.New("matrix must be a mapping")
	}

	for index := 0; index < len(matrixNode.Content); index += 2 {
		key := matrixNode.Content[index].Value
		node := matrixNode.Content[index+1]

		switch key {
		case "exclude":
			model.hasExclude = true
		case "include":
			model.hasInclude = true

			if node.Kind != yaml.SequenceNode {
				return matrixModel{}, errors.New("matrix include must be a sequence")
			}

			for _, include := range node.Content {
				if include.Kind != yaml.MappingNode {
					return matrixModel{}, errors.New("matrix include entry must be a mapping")
				}

				row := matrixRow{values: map[string]any{}}

				for pair := 0; pair < len(include.Content); pair += 2 {
					var entryValue any
					if err := include.Content[pair+1].Decode(&entryValue); err != nil {
						return matrixModel{}, err
					}

					entryKey := include.Content[pair].Value
					row.order = append(row.order, entryKey)
					row.values[entryKey] = entryValue
				}

				model.includeRows = append(model.includeRows, row)
			}
		default:
			model.keys = append(model.keys, key)
		}
	}

	return model, nil
}

func expandCoordinates(workflowID, jobID, jobName string, matrix matrixModel) ([]string, []string, error) {
	prefix := workflowID + "/" + jobID

	m, ok := matrix.value.(map[string]any)
	if !ok || len(m) == 0 {
		return []string{prefix}, []string{jobName}, nil
	}

	if matrix.hasExclude {
		return nil, nil, errors.New("matrix exclude semantics are unsupported")
	}

	if matrix.hasInclude && len(matrix.keys) != 0 {
		return nil, nil, errors.New("matrix include combined with cartesian axes is unsupported")
	}

	values := map[string][]any{}

	for _, key := range matrix.keys {
		value := m[key]
		if list, ok := value.([]any); ok {
			values[key] = list
		} else {
			return nil, nil, fmt.Errorf("matrix axis %s must be a sequence", key)
		}
	}

	var rows []matrixRow
	if len(matrix.keys) != 0 {
		rows = []matrixRow{{values: map[string]any{}, order: append([]string(nil), matrix.keys...)}}
	}

	for _, key := range matrix.keys {
		var next []matrixRow

		for _, row := range rows {
			for _, value := range values[key] {
				clone := make(map[string]any, len(row.values)+1)
				for rowKey, rowValue := range row.values {
					clone[rowKey] = rowValue
				}

				clone[key] = value
				next = append(next, matrixRow{values: clone, order: row.order})
			}
		}

		rows = next
	}

	rows = append(rows, matrix.includeRows...)

	if len(rows) == 0 {
		return []string{prefix}, []string{jobName}, nil
	}

	seen := map[string]bool{}

	var (
		result []string
		names  []string
	)

	for _, row := range rows {
		var parts []string
		for key, value := range row.values {
			parts = append(parts, fmt.Sprintf("%s=%v", key, value))
		}

		sort.Strings(parts)

		coordinate := prefix + "[" + strings.Join(parts, ",") + "]"
		if !seen[coordinate] {
			seen[coordinate], result = true, append(result, coordinate)
			names = append(names, renderGitHubJobName(jobID, jobName, row))
		}
	}

	type pair struct {
		coordinate string
		name       string
	}

	pairs := make([]pair, len(result))
	for index := range result {
		pairs[index] = pair{coordinate: result[index], name: names[index]}
	}

	sort.Slice(pairs, func(i, j int) bool { return pairs[i].coordinate < pairs[j].coordinate })

	for index := range pairs {
		result[index], names[index] = pairs[index].coordinate, pairs[index].name
	}

	return result, names, nil
}

func renderGitHubJobName(jobID, jobName string, row matrixRow) string {
	rendered := jobName
	for key, value := range row.values {
		rendered = strings.ReplaceAll(rendered, "${{ matrix."+key+" }}", fmt.Sprint(value))
		rendered = strings.ReplaceAll(rendered, "${{matrix."+key+"}}", fmt.Sprint(value))
	}

	if rendered != jobID || len(row.values) == 0 {
		return rendered
	}

	values := make([]string, 0, len(row.order))
	for _, key := range row.order {
		values = append(values, fmt.Sprint(row.values[key]))
	}

	return jobID + " (" + strings.Join(values, ", ") + ")"
}

func validateWorkflowFiles(repo string) error {
	entries, err := os.ReadDir(filepath.Join(repo, ".github", "workflows"))
	if err != nil {
		return fmt.Errorf("read workflow directory: %w", err)
	}

	expected := make(map[string]bool, len(workflowIDs))
	for _, id := range workflowIDs {
		expected[id+".yml"] = true
	}

	var extra []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension != ".yml" && extension != ".yaml" {
			continue
		}

		if !expected[entry.Name()] {
			extra = append(extra, entry.Name())
			continue
		}

		delete(expected, entry.Name())
	}

	if len(extra) != 0 || len(expected) != 0 {
		var missing []string
		for name := range expected {
			missing = append(missing, name)
		}

		sort.Strings(extra)
		sort.Strings(missing)

		return fmt.Errorf("workflow file set mismatch: missing=%v extra=%v", missing, extra)
	}

	return nil
}

func inventorySurfaces(repo string) ([]Surface, error) {
	pathspecs := append([]string{}, ciConfigurationPaths...)
	pathspecs = append(pathspecs, ciEntrypointPaths...)
	pathspecs = append(pathspecs, ciGoPolicySurfacePaths...)
	pathspecs = append(pathspecs, ".github/workflows/scripts", "scripts")
	args := append([]string{"-C", repo, "ls-files", "--stage", "-z", "--"}, pathspecs...)

	output, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked CI policy surfaces: %w", err)
	}

	configurations := make(map[string]bool, len(ciConfigurationPaths))
	for _, path := range ciConfigurationPaths {
		configurations[path] = true
	}

	entrypoints := make(map[string]bool, len(ciEntrypointPaths))
	for _, path := range ciEntrypointPaths {
		entrypoints[path] = true
	}

	var result []Surface

	seenPaths := map[string]bool{}

	for _, entry := range strings.Split(string(output), "\x00") {
		if entry == "" {
			continue
		}

		metadata, rel, found := strings.Cut(entry, "\t")

		fields := strings.Fields(metadata)
		if !found || len(fields) != 3 || fields[2] != "0" {
			return nil, fmt.Errorf("CI policy surface has a non-stage-zero index entry: %q", entry)
		}

		mode := fields[0]
		if mode != "100644" && mode != "100755" {
			return nil, fmt.Errorf("CI policy surface %s has unsupported indexed mode %s", rel, mode)
		}

		if seenPaths[rel] {
			return nil, fmt.Errorf("CI policy surface %s has multiple index entries", rel)
		}

		seenPaths[rel] = true

		data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("read CI policy surface %s: %w", rel, err)
		}

		kind := "root-script-policy"
		owner := "graith-maintainers"
		contract := "preserve the checked-in script's current executable behavior under its existing callers and tests"

		if configurations[rel] {
			kind = "ci-configuration"
			contract = "preserve the checked-in CI tool or release configuration consumed by current workflow proof"

			if rel == "libghostty-native.lock.json" {
				owner = "native-owners"
				contract = "preserve pinned native dependency commits, URLs, and SHA-256 values consumed by current producer and package proof"
			}
		} else if entrypoints[rel] {
			kind = "ci-entrypoint"
			contract = "preserve the checked-in make targets invoked directly by current workflow proof"

			if rel == "gui/ios/Makefile" {
				owner = "gui-owners"
			}
		} else if strings.HasPrefix(rel, ".github/workflows/scripts/") {
			kind = "workflow-helper"
			if strings.Contains(rel, ".test.") {
				kind = "workflow-contract-test"
				contract = "preserve the current workflow trust and behavior assertions until replaced by semantic Go contracts"
			}
		} else if metadata, ok := ciGoPolicySurfaces[rel]; ok {
			kind = metadata.kind
			contract = metadata.contract
		}

		retirement := "owned replacement has equivalent executable coverage and zero unexplained replay disagreement"
		if metadata, ok := ciGoPolicySurfaces[rel]; ok {
			retirement = metadata.retirement
		}

		result = append(result, Surface{
			Path: filepath.ToSlash(rel), Owner: owner, Kind: kind, GitMode: mode, SHA256: sum(data),
			Contract: contract, Disposition: "grandfathered",
			Retirement: retirement,
		})
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })

	return result, nil
}

func (inv *Inventory) setDigest() error {
	inv.Digest = ""

	data, err := json.Marshal(inv)
	if err != nil {
		return err
	}

	inv.Digest = sum(data)

	return nil
}

func (inv *Inventory) Validate() error {
	if inv.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema version %d", inv.SchemaVersion)
	}

	if inv.ObservationState != "p0-observation-in-progress" {
		return errors.New("inventory must remain marked p0-observation-in-progress")
	}

	expected := map[string]bool{}
	for _, id := range workflowIDs {
		expected[id] = true
	}

	coordinates := map[string]Job{}
	requiredJobs := map[string]int{}

	for _, workflow := range inv.Workflows {
		if !expected[workflow.ID] {
			return fmt.Errorf("extra workflow %q", workflow.ID)
		}

		delete(expected, workflow.ID)

		if workflow.Owner == "" {
			return fmt.Errorf("workflow %s has no owner", workflow.ID)
		}

		for _, job := range workflow.Jobs {
			if job.Owner == "" {
				return fmt.Errorf("job %s/%s has no owner", workflow.ID, job.ID)
			}

			if job.Capability == "" {
				return fmt.Errorf("job %s/%s has no capability", workflow.ID, job.ID)
			}

			for _, coordinate := range job.Coordinates {
				if _, exists := coordinates[coordinate]; exists {
					return fmt.Errorf("duplicate job coordinate %q", coordinate)
				}

				coordinates[coordinate] = job
			}

			if len(job.GitHubNames) != len(job.Coordinates) {
				return fmt.Errorf("job %s/%s has %d coordinates but %d GitHub names", workflow.ID, job.ID, len(job.Coordinates), len(job.GitHubNames))
			}

			if job.SkipSemantics != "github-skipped-is-distinct-from-passed" {
				return fmt.Errorf("job %s/%s has unsupported skip semantics %q", workflow.ID, job.ID, job.SkipSemantics)
			}

			if !oneOf(job.Requiredness, "required", "soft") {
				return fmt.Errorf("job %s/%s has unsupported requiredness %q", workflow.ID, job.ID, job.Requiredness)
			}

			if job.Requiredness == "required" {
				requiredJobs[job.Name]++
			}

			if !oneOf(job.ProofType, "source-level", "package-consumer", "compile-only", "runtime", "scheduled", "soft") {
				return fmt.Errorf("job %s/%s has unsupported proof type %q", workflow.ID, job.ID, job.ProofType)
			}
		}
	}

	if len(expected) != 0 {
		var missing []string
		for id := range expected {
			missing = append(missing, id)
		}

		sort.Strings(missing)

		return fmt.Errorf("missing workflows: %s", strings.Join(missing, ", "))
	}

	if len(inv.RequiredContexts) != len(requiredJobs) {
		return fmt.Errorf("required context/job count mismatch: %d contexts, %d jobs", len(inv.RequiredContexts), len(requiredJobs))
	}

	for _, context := range inv.RequiredContexts {
		if requiredJobs[context] != 1 {
			return fmt.Errorf("required context %q matches %d required jobs", context, requiredJobs[context])
		}
	}

	mapped := map[string]bool{}

	for _, mapping := range inv.Mappings {
		if mapping.Owner == "" {
			return fmt.Errorf("mapping %s has no owner", mapping.LegacyCoordinate)
		}

		if mapped[mapping.LegacyCoordinate] {
			return fmt.Errorf("duplicate mapping row %q", mapping.LegacyCoordinate)
		}

		mapped[mapping.LegacyCoordinate] = true

		job, exists := coordinates[mapping.LegacyCoordinate]
		if !exists {
			return fmt.Errorf("orphan mapping row %q", mapping.LegacyCoordinate)
		}

		if mapping.LegacyCondition != job.Condition || mapping.SkipSemantics != job.SkipSemantics {
			return fmt.Errorf("mapping %s does not preserve skip semantics", mapping.LegacyCoordinate)
		}

		if mapping.NewMode == "" && mapping.Retirement == "" {
			return fmt.Errorf("mapping %s has neither mode nor retirement", mapping.LegacyCoordinate)
		}

		if mapping.NewObligation && mapping.Justification == "" {
			return fmt.Errorf("new obligation %s is unjustified", mapping.NewMode)
		}
	}

	for coordinate := range coordinates {
		if !mapped[coordinate] {
			return fmt.Errorf("missing mapping for %q", coordinate)
		}
	}

	surfacePaths := map[string]bool{}

	for _, surface := range inv.Surfaces {
		if surface.Path == "" || surface.SHA256 == "" ||
			(surface.GitMode != "100644" && surface.GitMode != "100755") {
			return fmt.Errorf("surface %s has incomplete identity", surface.Path)
		}

		if surfacePaths[surface.Path] {
			return fmt.Errorf("duplicate policy surface %q", surface.Path)
		}

		surfacePaths[surface.Path] = true

		if surface.Owner == "" {
			return fmt.Errorf("surface %s has no owner", surface.Path)
		}

		if surface.Contract == "" || surface.Disposition == "" || surface.Retirement == "" {
			return fmt.Errorf("surface %s has incomplete contract metadata", surface.Path)
		}

		if !oneOf(surface.Disposition, "grandfathered", "retire") {
			return fmt.Errorf("surface %s has unsupported disposition %q", surface.Path, surface.Disposition)
		}
	}

	for _, path := range ciConfigurationPaths {
		if !surfacePaths[path] {
			return fmt.Errorf("missing CI configuration surface %q", path)
		}
	}

	for _, path := range ciEntrypointPaths {
		if !surfacePaths[path] {
			return fmt.Errorf("missing CI entrypoint surface %q", path)
		}
	}

	digestInventory := *inv
	if err := digestInventory.setDigest(); err != nil {
		return err
	}

	if inv.Digest != digestInventory.Digest {
		return fmt.Errorf("inventory digest mismatch: got %s want %s", inv.Digest, digestInventory.Digest)
	}

	return nil
}

func requireNamedSurfaces(surfaces []Surface, paths []string, label string) error {
	seen := map[string]bool{}
	for _, surface := range surfaces {
		seen[surface.Path] = true
	}

	for _, path := range paths {
		if !seen[path] {
			return fmt.Errorf("missing %s %q", label, path)
		}
	}

	return nil
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}

	return false
}

func canonical(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage("null"), nil
	}

	data, err := json.Marshal(value)

	return json.RawMessage(data), err
}

func sum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func workflowOwner(id string) string {
	switch id {
	case "gui-ci":
		return "gui-owners"
	case "libghostty-native", "libghostty-native-publish":
		return "native-owners"
	case "goreleaser", "dev-release", "release-please":
		return "release-owners"
	case "codeql", "scorecard", "secret-scan", "dependency-review":
		return "security-owners"
	case "docs", "docs-preview":
		return "docs-owners"
	default:
		return "graith-maintainers"
	}
}

var proofTypes = map[string]string{
	"ci/build": "compile-only", "ci/build-macos": "compile-only", "ci/changes": "source-level",
	"ci/govulncheck": "source-level", "ci/integration": "runtime", "ci/integration-macos": "runtime",
	"ci/ci-shadow-summary": "soft", "ci/lint": "compile-only", "ci/test": "runtime", "ci/test-macos": "runtime",
	"coverage/changes": "soft", "coverage/comment": "soft", "coverage/go-coverage": "soft",
	"coverage/swift-coverage": "soft", "gui-ci/build": "compile-only",
	"libghostty-native/apple-adapter": "runtime", "libghostty-native/changes": "source-level",
	"libghostty-native/linux-adapter": "compile-only", "libghostty-native/native-gate": "runtime",
	"libghostty-native-publish/publish": "scheduled", "regen/prepare": "source-level",
	"regen/regen": "runtime", "regen/validate": "source-level",
	"docs-preview/cleanup": "scheduled", "docs-preview/preview": "compile-only",
	"docs-preview/prune": "scheduled", "dev-release/assemble-dev": "package-consumer",
	"dev-release/attest-linux": "package-consumer", "dev-release/build-darwin": "package-consumer",
	"dev-release/build-linux": "package-consumer", "dev-release/changes": "source-level",
	"dev-release/execute-linux": "runtime", "dev-release/publish-dev": "scheduled",
	"dev-release/release-context": "source-level", "release-please/release-please": "scheduled",
	"goreleaser/assemble-stable": "package-consumer", "goreleaser/attest-stable": "package-consumer",
	"goreleaser/build-darwin": "package-consumer", "goreleaser/build-linux": "package-consumer",
	"goreleaser/changes": "source-level", "goreleaser/execute-linux": "runtime",
	"goreleaser/publish-stable": "scheduled", "goreleaser/release-context": "source-level",
	"sandbox/changes": "source-level", "sandbox/linux-nono": "runtime", "sandbox/macos-safehouse": "runtime",
	"dependency-review/dependency-review": "source-level", "codeql/analyze": "source-level",
	"scorecard/analysis": "source-level", "secret-scan/gitleaks": "source-level",
	"secret-scan/trufflehog": "source-level", "workflow-lint/actionlint": "source-level",
	"workflow-lint/renovate": "source-level", "workflow-lint/scripts": "runtime",
	"workflow-lint/shellcheck": "source-level", "workflow-lint/zizmor": "source-level",
	"docs/build": "compile-only", "docs/deploy": "scheduled", "commits/commitsar": "source-level",
}
