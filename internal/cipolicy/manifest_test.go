package cipolicy

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/atomicfile"
	"github.com/d0ugal/graith/internal/cibaseline"
)

var updateManifest = flag.Bool("update", false, "regenerate the committed cipolicy manifest")

func TestCommittedManifestMatchesInventory(t *testing.T) {
	if *updateManifest {
		updateCommittedManifest(t)
	}

	manifest := loadManifest(t)

	if err := ValidateCurrent(manifest, filepath.Join("..", "cibaseline", "inventory.json")); err != nil {
		t.Fatal(err)
	}

	inventory := loadInventory(t)

	var coordinateCount int

	for _, workflow := range inventory.Workflows {
		for _, job := range workflow.Jobs {
			coordinateCount += len(job.Coordinates)
		}
	}

	var manifestCoordinates int
	for _, mode := range manifest.Modes {
		manifestCoordinates += len(mode.Coordinates)
	}

	if manifestCoordinates != coordinateCount {
		t.Fatalf("manifest coordinates = %d, want P0 coordinate count %d", manifestCoordinates, coordinateCount)
	}

	if len(manifest.Unsupported) != 1 ||
		manifest.Unsupported[0].Mode != "dynamic/dependabot/update-graph" ||
		manifest.Unsupported[0].Owner != "graith-maintainers" ||
		manifest.Unsupported[0].Expires != "2026-08-31" ||
		!slices.Contains(manifest.Unsupported[0].EvidenceRefs, "p0-acceptance:gap-external-dependabot-update-graph-30152132020") {
		t.Fatalf("external observed mode row = %#v, want owned expiring unsupported decision", manifest.Unsupported)
	}
}

func updateCommittedManifest(t *testing.T) {
	t.Helper()

	inventoryPath := filepath.Join("..", "cibaseline", "inventory.json")

	manifest, err := GenerateFromInventoryPath(inventoryPath)
	if err != nil {
		t.Fatal(err)
	}

	data, err := manifest.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeManifest("generated policy manifest", data)
	if err != nil {
		t.Fatal(err)
	}

	if err := ValidateCurrent(decoded, inventoryPath); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.Write("manifest.json", data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestJobEventConditionsNarrowTrustTiers(t *testing.T) {
	manifest := generatedManifest(t)

	tests := map[string]struct {
		events     []string
		trustTiers []string
	}{
		"legacy/coverage/comment":                  {events: []string{"pull-request"}, trustTiers: []string{"same-repository-agent", "trusted-base"}},
		"legacy/dev-release/build-linux":           {events: []string{"pull-request", "push-main"}, trustTiers: []string{"fork-untrusted", "same-repository-agent", "trusted-base"}},
		"legacy/dev-release/publish-dev":           {events: []string{"push-main"}, trustTiers: []string{"trusted-base", "trusted-publication"}},
		"legacy/docs-preview/preview":              {events: []string{"pull-request"}, trustTiers: []string{"fork-untrusted", "same-repository-agent", "trusted-base"}},
		"legacy/docs-preview/prune":                {events: []string{"schedule"}, trustTiers: []string{"trusted-base"}},
		"legacy/docs-preview/cleanup":              {events: []string{"pull-request"}, trustTiers: []string{"fork-untrusted", "same-repository-agent", "trusted-base"}},
		"legacy/goreleaser/build-linux":            {events: []string{"pull-request", "push-tag"}, trustTiers: []string{"fork-untrusted", "same-repository-agent", "trusted-base"}},
		"legacy/goreleaser/publish-stable":         {events: []string{"push-tag"}, trustTiers: []string{"trusted-publication"}},
		"legacy/libghostty-native-publish/publish": {events: []string{"push-main", "workflow-dispatch"}, trustTiers: []string{"trusted-base", "trusted-publication"}},
		"legacy/regen/prepare":                     {events: []string{"pull-request"}, trustTiers: []string{"same-repository-agent", "trusted-base"}},
		"legacy/release-please/release-please":     {events: []string{"push-main"}, trustTiers: []string{"trusted-base"}},
	}

	for modeID, test := range tests {
		t.Run(modeID, func(t *testing.T) {
			mode := findMode(t, manifest, modeID)

			var got []string
			for _, sourceEvent := range mode.SourceEvents {
				got = append(got, sourceEvent.Event)
			}

			if strings.Join(got, ",") != strings.Join(test.events, ",") {
				t.Fatalf("events = %v, want %v", got, test.events)
			}

			if strings.Join(mode.TrustTiers, ",") != strings.Join(test.trustTiers, ",") {
				t.Fatalf("trust tiers = %v, want %v", mode.TrustTiers, test.trustTiers)
			}
		})
	}
}

func TestProtectedPublicationTrustIsExplicitlyAllowlisted(t *testing.T) {
	manifest := generatedManifest(t)

	wantPublication := map[string]bool{
		"legacy/dev-release/publish-dev":           true,
		"legacy/docs/deploy":                       true,
		"legacy/goreleaser/publish-stable":         true,
		"legacy/libghostty-native-publish/publish": true,
	}

	for _, mode := range manifest.Modes {
		hasPublication := slices.Contains(mode.TrustTiers, "trusted-publication")
		if hasPublication != wantPublication[mode.ID] {
			t.Fatalf("%s trusted-publication = %v, want %v", mode.ID, hasPublication, wantPublication[mode.ID])
		}

		for _, sourceEvent := range mode.SourceEvents {
			if sourceEvent.Event == "pull-request" && hasPublication {
				t.Fatalf("%s pull-request mode has trusted-publication trust", mode.ID)
			}
		}
	}
}

func TestEventsForJobRejectsUnsupportedOrMismatchedConditions(t *testing.T) {
	events, err := eventsForJob(
		[]string{"pull-request", "push-main"},
		"github.event_name == 'pull_request' || github.event_name == 'push'",
	)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"pull-request", "push-main"}; !slices.Equal(events, want) {
		t.Fatalf("compound events = %v, want %v", events, want)
	}

	events, err = eventsForJob(
		[]string{"push-main", "push-tag", "workflow-dispatch"},
		"github.event_name == 'workflow_dispatch' || github.ref == 'refs/heads/main'",
	)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"push-main", "workflow-dispatch"}; !slices.Equal(events, want) {
		t.Fatalf("main-ref condition events = %v, want %v", events, want)
	}

	tests := map[string]struct {
		workflowEvents []string
		condition      string
		want           string
	}{
		"mismatched condition": {
			workflowEvents: []string{"pull-request"},
			condition:      "github.event_name == 'schedule'",
			want:           "does not intersect workflow events",
		},
		"unsupported explicit event": {
			workflowEvents: []string{"pull-request"},
			condition:      "github.event_name == 'merge_group'",
			want:           "unsupported github.event_name",
		},
		"unsupported policy token": {
			workflowEvents: []string{"pull-request"},
			condition:      "inputs.thrawn",
			want:           "unsupported policy condition",
		},
		"unsupported token beside explicit event": {
			workflowEvents: []string{"pull-request"},
			condition:      "github.event_name == 'pull_request' && inputs.thrawn",
			want:           "unsupported policy condition",
		},
		"unsupported ref beside same-repository guard": {
			workflowEvents: []string{"pull-request"},
			condition:      "github.event.pull_request.head.repo.full_name == github.repository && github.ref == 'refs/heads/main'",
			want:           "unsupported policy condition",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := eventsForJob(test.workflowEvents, test.condition)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("eventsForJob() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestWorkflowEventIDsRejectsClosedWorldDrift(t *testing.T) {
	tests := map[string]struct {
		raw  string
		want string
	}{
		"unknown event beside known event": {
			raw:  `{"pull_request":null,"merge_group":null}`,
			want: `unsupported workflow event "merge_group"`,
		},
		"unrestricted push": {
			raw:  `{"push":null}`,
			want: "unsupported unrestricted push identity",
		},
		"unsupported branch beside main": {
			raw:  `{"push":{"branches":["main","release"]}}`,
			want: `unsupported push branch identity "release"`,
		},
		"unsupported tag beside release tag": {
			raw:  `{"push":{"tags":["v*","latest"]}}`,
			want: `unsupported push tag identity "latest"`,
		},
		"unsupported push key": {
			raw:  `{"push":{"branches":["main"],"branches-ignore":["dreich"]}}`,
			want: `unsupported push identity key "branches-ignore"`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := workflowEventIDs(json.RawMessage(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("workflowEventIDs() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestModeFromJobRejectsMissingGitHubNames(t *testing.T) {
	inventory := loadInventory(t)
	mappings := make(map[string]cibaseline.Mapping, len(inventory.Mappings))

	for _, mapping := range inventory.Mappings {
		mappings[mapping.LegacyCoordinate] = mapping
	}

	workflow := inventory.Workflows[0]
	job := workflow.Jobs[0]
	job.GitHubNames = nil

	_, err := modeFromJob(inventory, workflow, job, mappings, []string{"pull-request"})
	if err == nil || !strings.Contains(err.Error(), "missing GitHub name") {
		t.Fatalf("modeFromJob() error = %v, want missing GitHub name", err)
	}
}

func TestPlatformForJobRejectsUnknownRunnerIdentities(t *testing.T) {
	tests := map[string]struct {
		job    cibaseline.Job
		matrix map[string]string
		want   string
	}{
		"literal runner": {
			job:  cibaseline.Job{Runner: json.RawMessage(`"windows-latest"`)},
			want: `unknown runner platform "windows-latest"`,
		},
		"matrix runner": {
			job:    cibaseline.Job{Runner: json.RawMessage(`"${{ matrix.runner }}"`)},
			matrix: map[string]string{"runner": "windows-latest"},
			want:   `unknown runner platform "windows-latest"`,
		},
		"missing matrix runner": {
			job:  cibaseline.Job{Runner: json.RawMessage(`"${{ matrix.runner }}"`)},
			want: "matrix runner is missing",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := platformForJob(test.job, test.matrix)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("platformForJob() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDigestIsDeterministicAcrossInputOrderVariation(t *testing.T) {
	base := generatedManifest(t)

	want, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}

	shuffled := cloneManifest(t, base)
	slices.Reverse(shuffled.TrustTiers)
	slices.Reverse(shuffled.Events)
	slices.Reverse(shuffled.Platforms)
	slices.Reverse(shuffled.CostClasses)
	slices.Reverse(shuffled.Capabilities)
	slices.Reverse(shuffled.Modes)
	slices.Reverse(shuffled.Unsupported)

	for index := range shuffled.Capabilities {
		slices.Reverse(shuffled.Capabilities[index].Modes)
	}

	for index := range shuffled.Modes {
		slices.Reverse(shuffled.Modes[index].SourceEvents)
		slices.Reverse(shuffled.Modes[index].TrustTiers)
		slices.Reverse(shuffled.Modes[index].Coordinates)
		slices.Reverse(shuffled.Modes[index].EvidenceRefs)

		for coordinateIndex := range shuffled.Modes[index].Coordinates {
			slices.Reverse(shuffled.Modes[index].Coordinates[coordinateIndex].EvidenceRefs)
		}
	}

	got, err := shuffled.Digest()
	if err != nil {
		t.Fatal(err)
	}

	if got != want {
		t.Fatalf("shuffled digest = %s, want %s", got, want)
	}

	canonical, err := shuffled.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeManifest("canny-policy.json", canonical)
	if err != nil {
		t.Fatal(err)
	}

	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}

	if decoded.PolicyDigest != want {
		t.Fatalf("canonical digest = %s, want %s", decoded.PolicyDigest, want)
	}
}

func TestCanonicalizationDoesNotMutateInput(t *testing.T) {
	manifest := generatedManifest(t)
	slices.Reverse(manifest.Events)
	slices.Reverse(manifest.Modes)
	slices.Reverse(manifest.Unsupported)

	before, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manifest.Digest(); err != nil {
		t.Fatal(err)
	}

	if _, err := manifest.MarshalCanonical(); err != nil {
		t.Fatal(err)
	}

	after, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	if string(after) != string(before) {
		t.Fatalf("canonicalization mutated input:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestManifestValidationFailures(t *testing.T) {
	base := generatedManifest(t)
	signManifest(t, &base)

	requiredMode, requiredCoordinate := findRequiredCoordinate(t, base)
	softMode, softCoordinate := findCoordinateByRequiredness(t, base, "soft")
	wrongDigest := strings.Repeat("1", 64)

	tests := map[string]struct {
		edit func(*Manifest)
		want string
	}{
		"unknown mode reference": {
			edit: func(manifest *Manifest) {
				manifest.Capabilities[0].Modes = append(manifest.Capabilities[0].Modes, "legacy/dreich/blether")
			},
			want: "capability references unknown mode",
		},
		"duplicate mode ID": {
			edit: func(manifest *Manifest) {
				manifest.Modes[1].ID = manifest.Modes[0].ID
			},
			want: "duplicate mode ID",
		},
		"duplicate coordinate": {
			edit: func(manifest *Manifest) {
				manifest.Modes[1].Coordinates[0].ID = manifest.Modes[0].Coordinates[0].ID
			},
			want: "duplicate coordinate",
		},
		"missing owner": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].Owner = ""
			},
			want: "has no owner",
		},
		"baseline path drift": {
			edit: func(manifest *Manifest) {
				manifest.Source.BaselineInventory.Path = "internal/cibaseline/dreich.json"
			},
			want: "baseline inventory path",
		},
		"baseline schema drift": {
			edit: func(manifest *Manifest) {
				manifest.Source.BaselineInventory.SchemaVersion = 1
			},
			want: "baseline inventory schema version",
		},
		"baseline observation drift": {
			edit: func(manifest *Manifest) {
				manifest.Source.BaselineInventory.ObservationState = "accepted"
			},
			want: "observation state",
		},
		"ambiguous requiredness": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].Requiredness = ""
			},
			want: "ambiguous requiredness",
		},
		"mode trust tier not allowed by source events": {
			edit: func(manifest *Manifest) {
				mode := findModePointer(t, manifest, "legacy/docs-preview/preview")
				mode.TrustTiers = append(mode.TrustTiers, "trusted-publication")
			},
			want: "not allowed by source events",
		},
		"mode trust tier does not overlap every source event": {
			edit: func(manifest *Manifest) {
				mode := findModePointer(t, manifest, "legacy/libghostty-native-publish/publish")
				mode.TrustTiers = []string{"trusted-publication"}
			},
			want: "has no trust tier allowed by event",
		},
		"missing mode trace": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].Trace.InventoryMapping = ""
			},
			want: "incomplete P0 trace",
		},
		"unsupported silent pass": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].SilentPassAllowed = true
			},
			want: "cannot allow a silent pass",
		},
		"unsupported missing rationale": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].Rationale = ""
			},
			want: "lacks owner, rationale, or expiry",
		},
		"unsupported collides with required coordinate": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].Mode = requiredMode
				manifest.Unsupported[0].Coordinate = requiredCoordinate
			},
			want: "collides with live mode",
		},
		"unsupported collides with soft coordinate": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].Mode = softMode
				manifest.Unsupported[0].Coordinate = softCoordinate
			},
			want: "collides with live mode",
		},
		"unsupported unknown mode": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].Mode = "legacy/dreich/blether"
			},
			want: "references unknown mode",
		},
		"unsupported unknown dynamic mode": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].Mode = "dynamic/dreich/blether"
			},
			want: "references unknown mode",
		},
		"unsupported malformed coordinate": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].Coordinate = "legacy/dreich/blether[runner]"
				manifest.Unsupported[0].Mode = ""
			},
			want: "invalid coordinate",
		},
		"unsupported duplicate coordinate": {
			edit: func(manifest *Manifest) {
				duplicate := manifest.Unsupported[0]
				duplicate.Mode = "dynamic/dreich/blether"
				manifest.Unsupported = append(manifest.Unsupported, duplicate)
			},
			want: "duplicate unsupported coordinate",
		},
		"unsupported duplicate coordinate with different identity": {
			edit: func(manifest *Manifest) {
				duplicate := manifest.Unsupported[0]
				duplicate.Mode = ""
				duplicate.Event = "pull-request"
				duplicate.Platform = "ubuntu-latest"
				duplicate.TrustTier = "trusted-base"
				manifest.Unsupported = append(manifest.Unsupported, duplicate)
			},
			want: "duplicate unsupported coordinate",
		},
		"unsupported trust tier not allowed by event": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].TrustTier = "trusted-base"
			},
			want: "not allowed by event",
		},
		"invalid trust tier": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].TrustTiers[0] = "thrawn"
			},
			want: "unknown trust tier",
		},
		"invalid platform": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].Coordinates[0].Platform = "bothy"
			},
			want: "unknown platform",
		},
		"invalid cost class": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].CostClass = "croft"
			},
			want: "unknown cost class",
		},
		"invalid source identity": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].SourceEvents[0].Source = "strath"
			},
			want: "invalid source",
		},
		"invalid event": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].SourceEvents[0].Event = "dreich"
			},
			want: "unknown event",
		},
		"invalid coordinate identity": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].Coordinates[0].ID = "legacy/dreich/blether[runner]"
			},
			want: "invalid coordinate",
		},
		"duplicate GitHub name": {
			edit: func(manifest *Manifest) {
				manifest.Modes[1].Coordinates[0].GitHubName = manifest.Modes[0].Coordinates[0].GitHubName
			},
			want: "duplicate GitHub name",
		},
		"coordinate GitHub check-name evidence mismatch": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].Coordinates[0].EvidenceRefs[1] = "github-check-name:Blether"
			},
			want: "does not match",
		},
		"coordinate runner platform mismatch": {
			edit: func(manifest *Manifest) {
				coordinate := findCoordinateWithMatrixKey(t, manifest, "runner")
				coordinate.Platform = "macos-26"
			},
			want: "runner matrix platform",
		},
		"invalid evidence reference": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].Coordinates[0].EvidenceRefs[0] = "blether"
			},
			want: "invalid evidence reference",
		},
		"P0 inventory digest mismatch": {
			edit: func(manifest *Manifest) {
				manifest.Modes[0].EvidenceRefs[0] = "p0-inventory:" + wrongDigest + "#canny"
			},
			want: "P0 inventory digest",
		},
		"digest mismatch": {
			edit: func(manifest *Manifest) {
				manifest.PolicyDigest = strings.Repeat("0", 64)
			},
			want: "policy digest mismatch",
		},
		"unsupported expired": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].Expires = "2026-07-25"
			},
			want: "expired on",
		},
		"unsupported expiry beyond renewal window": {
			edit: func(manifest *Manifest) {
				manifest.Unsupported[0].Expires = "9999-12-31"
			},
			want: "three-month renewal window",
		},
		"empty mode section": {
			edit: func(manifest *Manifest) {
				manifest.Modes = nil
			},
			want: "no modes declared",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := cloneManifest(t, base)
			test.edit(&manifest)

			if name != "digest mismatch" {
				signManifest(t, &manifest)
			}

			if err := manifest.ValidateAt(time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateAtRequiresValidationTime(t *testing.T) {
	manifest := generatedManifest(t)
	signManifest(t, &manifest)

	if err := manifest.ValidateAt(time.Time{}); err == nil || !strings.Contains(err.Error(), "validation time is required") {
		t.Fatalf("ValidateAt(time.Time{}) error = %v, want validation time rejection", err)
	}
}

func TestValidateCurrentRejectsGeneratedManifestDrift(t *testing.T) {
	manifest := generatedManifest(t)
	manifest.Capabilities[0].Description = "canny"
	signManifest(t, &manifest)

	if err := ValidateCurrent(manifest, filepath.Join("..", "cibaseline", "inventory.json")); err == nil ||
		!strings.Contains(err.Error(), "policy manifest is stale") {
		t.Fatalf("ValidateCurrent() error = %v, want stale manifest rejection", err)
	}
}

func TestValidateCurrentRejectsRequiredContextDrift(t *testing.T) {
	manifest := generatedManifest(t)
	requiredModeID, _ := findRequiredCoordinate(t, manifest)
	mode := findModePointer(t, &manifest, requiredModeID)

	mode.Requiredness = "soft"
	for index := range mode.Coordinates {
		mode.Coordinates[index].Requiredness = "soft"
	}

	signManifest(t, &manifest)

	if err := ValidateCurrent(manifest, filepath.Join("..", "cibaseline", "inventory.json")); err == nil ||
		!strings.Contains(err.Error(), "required contexts mismatch") {
		t.Fatalf("ValidateCurrent() error = %v, want required context drift rejection", err)
	}
}

func generatedManifest(t *testing.T) Manifest {
	t.Helper()

	manifest, err := FromInventory(loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	return manifest
}

func loadInventory(t *testing.T) cibaseline.Inventory {
	t.Helper()

	inventory, err := ReadInventory(filepath.Join("..", "cibaseline", "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}

	return inventory
}

func loadManifest(t *testing.T) Manifest {
	t.Helper()

	data, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := DecodeManifest("manifest.json", data)
	if err != nil {
		t.Fatal(err)
	}

	return manifest
}

func cloneManifest(t *testing.T, manifest Manifest) Manifest {
	t.Helper()

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	var clone Manifest
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}

	return clone
}

func signManifest(t *testing.T, manifest *Manifest) {
	t.Helper()

	digest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}

	manifest.PolicyDigest = digest
}

func findRequiredCoordinate(t *testing.T, manifest Manifest) (string, string) {
	t.Helper()

	return findCoordinateByRequiredness(t, manifest, "required")
}

func findCoordinateByRequiredness(t *testing.T, manifest Manifest, requiredness string) (string, string) {
	t.Helper()

	for _, mode := range manifest.Modes {
		if mode.Requiredness != requiredness {
			continue
		}

		for _, coordinate := range mode.Coordinates {
			if coordinate.Requiredness == requiredness {
				return mode.ID, coordinate.ID
			}
		}
	}

	t.Fatalf("missing %s coordinate", requiredness)

	return "", ""
}

func findMode(t *testing.T, manifest Manifest, id string) Mode {
	t.Helper()

	for _, mode := range manifest.Modes {
		if mode.ID == id {
			return mode
		}
	}

	t.Fatalf("missing mode %s", id)

	return Mode{}
}

func findModePointer(t *testing.T, manifest *Manifest, id string) *Mode {
	t.Helper()

	for index := range manifest.Modes {
		if manifest.Modes[index].ID == id {
			return &manifest.Modes[index]
		}
	}

	t.Fatalf("missing mode %s", id)

	return nil
}

func findCoordinateWithMatrixKey(t *testing.T, manifest *Manifest, key string) *Coordinate {
	t.Helper()

	for modeIndex := range manifest.Modes {
		for coordinateIndex := range manifest.Modes[modeIndex].Coordinates {
			coordinate := &manifest.Modes[modeIndex].Coordinates[coordinateIndex]
			if _, exists := coordinate.Matrix[key]; exists {
				return coordinate
			}
		}
	}

	t.Fatalf("missing coordinate with matrix key %s", key)

	return nil
}
