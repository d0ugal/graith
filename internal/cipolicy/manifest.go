// Package cipolicy owns the versioned CI north-star P1 policy manifest.
package cipolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const (
	SchemaVersion = 1
	PolicyVersion = "ci-policy-v1"

	DefaultManifestPath  = "internal/cipolicy/manifest.json"
	DefaultInventoryPath = "internal/cibaseline/inventory.json"
	DefaultRepository    = "d0ugal/graith"
	DefaultDefaultBranch = "main"

	sourceID = "github-actions"
)

type Manifest struct {
	SchemaVersion int                   `json:"schema_version"`
	PolicyVersion string                `json:"policy_version"`
	PolicyDigest  string                `json:"policy_digest"`
	Source        SourceIdentity        `json:"source"`
	TrustTiers    []TrustTier           `json:"trust_tiers"`
	Events        []EventIdentity       `json:"events"`
	Platforms     []Platform            `json:"platforms"`
	CostClasses   []CostClass           `json:"cost_classes"`
	Capabilities  []Capability          `json:"capabilities"`
	Modes         []Mode                `json:"modes"`
	Unsupported   []UnsupportedDecision `json:"unsupported"`
}

type SourceIdentity struct {
	ID                string            `json:"id"`
	Repository        string            `json:"repository"`
	DefaultBranch     string            `json:"default_branch"`
	PolicyPath        string            `json:"policy_path"`
	BaselineInventory BaselineInventory `json:"baseline_inventory"`
}

type BaselineInventory struct {
	Path             string `json:"path"`
	SchemaVersion    int    `json:"schema_version"`
	Digest           string `json:"digest"`
	ObservationState string `json:"observation_state"`
}

type TrustTier struct {
	ID                     string `json:"id"`
	Owner                  string `json:"owner"`
	Description            string `json:"description"`
	PublicationCredentials bool   `json:"publication_credentials"`
}

type EventIdentity struct {
	ID          string   `json:"id"`
	Source      string   `json:"source"`
	GitHubEvent string   `json:"github_event"`
	Refs        []string `json:"refs"`
	TrustTiers  []string `json:"trust_tiers"`
	Description string   `json:"description"`
}

type Platform struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	RunnerLabel  string `json:"runner_label"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Description  string `json:"description"`
}

type CostClass struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Description string `json:"description"`
}

type Capability struct {
	ID          string   `json:"id"`
	Owner       string   `json:"owner"`
	Description string   `json:"description"`
	Modes       []string `json:"modes"`
}

type Mode struct {
	ID           string        `json:"id"`
	Capability   string        `json:"capability"`
	Owner        string        `json:"owner"`
	Requiredness string        `json:"requiredness"`
	CostClass    string        `json:"cost_class"`
	ProofType    string        `json:"proof_type"`
	SourceEvents []SourceEvent `json:"source_events"`
	TrustTiers   []string      `json:"trust_tiers"`
	Coordinates  []Coordinate  `json:"coordinates"`
	EvidenceRefs []string      `json:"evidence_refs"`
	Trace        LegacyTrace   `json:"trace"`
}

type SourceEvent struct {
	Source string `json:"source"`
	Event  string `json:"event"`
}

type Coordinate struct {
	ID           string            `json:"id"`
	Platform     string            `json:"platform"`
	Matrix       map[string]string `json:"matrix"`
	Requiredness string            `json:"requiredness"`
	GitHubName   string            `json:"github_name"`
	EvidenceRefs []string          `json:"evidence_refs"`
	Trace        LegacyTrace       `json:"trace"`
}

type LegacyTrace struct {
	InventoryMapping string `json:"inventory_mapping"`
	LegacyWorkflow   string `json:"legacy_workflow"`
	LegacyJob        string `json:"legacy_job"`
	WorkflowPath     string `json:"workflow_path"`
	WorkflowSHA256   string `json:"workflow_sha256"`
	LegacyCondition  string `json:"legacy_condition"`
	SkipSemantics    string `json:"skip_semantics"`
}

type UnsupportedDecision struct {
	Mode              string   `json:"mode"`
	Coordinate        string   `json:"coordinate"`
	Source            string   `json:"source"`
	Event             string   `json:"event"`
	Platform          string   `json:"platform"`
	TrustTier         string   `json:"trust_tier"`
	Requiredness      string   `json:"requiredness"`
	Owner             string   `json:"owner"`
	Rationale         string   `json:"rationale"`
	Expires           string   `json:"expires"`
	SilentPassAllowed bool     `json:"silent_pass_allowed"`
	EvidenceRefs      []string `json:"evidence_refs"`
}

func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}

	return DecodeManifest(path, data)
}

func DecodeManifest(name string, data []byte) (Manifest, error) {
	var manifest Manifest

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", name, err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, fmt.Errorf("decode %s: trailing JSON value", name)
		}

		return Manifest{}, fmt.Errorf("decode %s: %w", name, err)
	}

	return manifest, nil
}

func (manifest Manifest) Canonical() Manifest {
	canonical := manifest.copy()

	sort.Slice(canonical.TrustTiers, func(i, j int) bool {
		return canonical.TrustTiers[i].ID < canonical.TrustTiers[j].ID
	})
	sort.Slice(canonical.Events, func(i, j int) bool { return canonical.Events[i].ID < canonical.Events[j].ID })
	sort.Slice(canonical.Platforms, func(i, j int) bool {
		return canonical.Platforms[i].ID < canonical.Platforms[j].ID
	})
	sort.Slice(canonical.CostClasses, func(i, j int) bool {
		return canonical.CostClasses[i].ID < canonical.CostClasses[j].ID
	})
	sort.Slice(canonical.Capabilities, func(i, j int) bool {
		return canonical.Capabilities[i].ID < canonical.Capabilities[j].ID
	})
	sort.Slice(canonical.Modes, func(i, j int) bool { return canonical.Modes[i].ID < canonical.Modes[j].ID })
	sort.SliceStable(canonical.Unsupported, func(i, j int) bool {
		left := unsupportedKey(canonical.Unsupported[i])
		right := unsupportedKey(canonical.Unsupported[j])

		return left < right
	})

	for index := range canonical.Events {
		canonical.Events[index].Refs = sortedStrings(canonical.Events[index].Refs)
		canonical.Events[index].TrustTiers = sortedStrings(canonical.Events[index].TrustTiers)
	}

	for index := range canonical.Capabilities {
		canonical.Capabilities[index].Modes = sortedStrings(canonical.Capabilities[index].Modes)
	}

	for index := range canonical.Modes {
		mode := &canonical.Modes[index]
		mode.TrustTiers = sortedStrings(mode.TrustTiers)
		mode.EvidenceRefs = sortedStrings(mode.EvidenceRefs)
		sort.Slice(mode.SourceEvents, func(i, j int) bool {
			if mode.SourceEvents[i].Source == mode.SourceEvents[j].Source {
				return mode.SourceEvents[i].Event < mode.SourceEvents[j].Event
			}

			return mode.SourceEvents[i].Source < mode.SourceEvents[j].Source
		})
		sort.Slice(mode.Coordinates, func(i, j int) bool {
			return mode.Coordinates[i].ID < mode.Coordinates[j].ID
		})

		for coordinateIndex := range mode.Coordinates {
			coordinate := &mode.Coordinates[coordinateIndex]
			if coordinate.Matrix == nil {
				coordinate.Matrix = map[string]string{}
			}

			coordinate.EvidenceRefs = sortedStrings(coordinate.EvidenceRefs)
		}
	}

	for index := range canonical.Unsupported {
		canonical.Unsupported[index].EvidenceRefs = sortedStrings(canonical.Unsupported[index].EvidenceRefs)
	}

	if canonical.Unsupported == nil {
		canonical.Unsupported = []UnsupportedDecision{}
	}

	return canonical
}

func (manifest Manifest) copy() Manifest {
	clone := manifest
	clone.TrustTiers = append([]TrustTier(nil), manifest.TrustTiers...)
	clone.Platforms = append([]Platform(nil), manifest.Platforms...)
	clone.CostClasses = append([]CostClass(nil), manifest.CostClasses...)

	clone.Events = append([]EventIdentity(nil), manifest.Events...)
	for index := range clone.Events {
		clone.Events[index].Refs = append([]string(nil), manifest.Events[index].Refs...)
		clone.Events[index].TrustTiers = append([]string(nil), manifest.Events[index].TrustTiers...)
	}

	clone.Capabilities = append([]Capability(nil), manifest.Capabilities...)
	for index := range clone.Capabilities {
		clone.Capabilities[index].Modes = append([]string(nil), manifest.Capabilities[index].Modes...)
	}

	clone.Modes = append([]Mode(nil), manifest.Modes...)
	for index := range clone.Modes {
		clone.Modes[index].SourceEvents = append([]SourceEvent(nil), manifest.Modes[index].SourceEvents...)
		clone.Modes[index].TrustTiers = append([]string(nil), manifest.Modes[index].TrustTiers...)
		clone.Modes[index].EvidenceRefs = append([]string(nil), manifest.Modes[index].EvidenceRefs...)
		clone.Modes[index].Coordinates = append([]Coordinate(nil), manifest.Modes[index].Coordinates...)

		for coordinateIndex := range clone.Modes[index].Coordinates {
			coordinate := manifest.Modes[index].Coordinates[coordinateIndex]

			clone.Modes[index].Coordinates[coordinateIndex].EvidenceRefs = append([]string(nil), coordinate.EvidenceRefs...)

			if coordinate.Matrix != nil {
				matrix := make(map[string]string, len(coordinate.Matrix))
				for key, value := range coordinate.Matrix {
					matrix[key] = value
				}

				clone.Modes[index].Coordinates[coordinateIndex].Matrix = matrix
			}
		}
	}

	clone.Unsupported = append([]UnsupportedDecision(nil), manifest.Unsupported...)
	for index := range clone.Unsupported {
		clone.Unsupported[index].EvidenceRefs = append([]string(nil), manifest.Unsupported[index].EvidenceRefs...)
	}

	return clone
}

func (manifest Manifest) Digest() (string, error) {
	canonical := manifest.Canonical()
	canonical.PolicyDigest = ""

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:]), nil
}

func (manifest Manifest) MarshalCanonical() ([]byte, error) {
	canonical := manifest.Canonical()

	digest, err := canonical.Digest()
	if err != nil {
		return nil, err
	}

	canonical.PolicyDigest = digest

	data, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(data, '\n'), nil
}

func sortedStrings(values []string) []string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)

	result := make([]string, 0, len(sorted))
	for _, value := range sorted {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}

	if result == nil {
		return []string{}
	}

	return result
}

func unsupportedKey(decision UnsupportedDecision) string {
	return strings.Join([]string{decision.Source, decision.Event, decision.Mode, decision.Coordinate, decision.Platform, decision.TrustTier}, "\x00")
}
