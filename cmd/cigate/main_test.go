package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/cigate"
	"github.com/d0ugal/graith/internal/cipolicy"
)

func TestRunRequiresInput(t *testing.T) {
	err := run([]string{"evaluate"})
	if err == nil || !strings.Contains(err.Error(), "-input is required") {
		t.Fatalf("run() error = %v, want input requirement", err)
	}
}

func TestEvaluateRequiresReplayStoreFlag(t *testing.T) {
	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "braw-evaluation.json")

	if err := os.WriteFile(input, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"-input", input, "evaluate"})
	if err == nil || !strings.Contains(err.Error(), "-replay-store is required") {
		t.Fatalf("run() error = %v, want replay-store requirement", err)
	}
}

func TestEvaluateRequiresTrustedPolicyAndDigestFlags(t *testing.T) {
	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "braw-evaluation.json")
	replay := filepath.Join(tempDir, "replay.json")

	if err := os.WriteFile(input, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-input", input, "-replay-store", replay}
	args = append(args, trustedConfigArgs(t, tempDir)...)
	args = append(args, "evaluate")

	err := run(args)
	if err == nil || !strings.Contains(err.Error(), "-trusted-policy is required") {
		t.Fatalf("run() error = %v, want trusted-policy requirement", err)
	}

	args = []string{
		"-input", input,
		"-replay-store", replay,
		"-trusted-policy", filepath.Join("..", "..", "internal", "cipolicy", "manifest.json"),
	}
	args = append(args, trustedConfigArgs(t, tempDir)...)
	args = append(args, "evaluate")

	err = run(args)
	if err == nil || !strings.Contains(err.Error(), "-policy-digest is required") {
		t.Fatalf("run() error = %v, want policy-digest requirement", err)
	}
}

func TestEvaluateRequiresTrustedConfigDigestFlag(t *testing.T) {
	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "braw-evaluation.json")
	replay := filepath.Join(tempDir, "replay.json")
	configPath, _ := writeTrustedConfig(t, tempDir, digestHex("release"), digestHex("evaluator"))
	manifest := trustedManifest(t)

	if err := os.WriteFile(input, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run([]string{
		"-input", input,
		"-replay-store", replay,
		"-trusted-config", configPath,
		"-trusted-policy", filepath.Join("..", "..", "internal", "cipolicy", "manifest.json"),
		"-policy-digest", manifest.PolicyDigest,
		"evaluate",
	})
	if err == nil || !strings.Contains(err.Error(), "-config-digest is required") {
		t.Fatalf("run() error = %v, want config-digest requirement", err)
	}
}

func TestEvaluateWritesFailurePayload(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "canny-evaluation.json")
	output := filepath.Join(tempDir, "canny-output.json")
	replay := filepath.Join(tempDir, "replay.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	if err := os.WriteFile(input, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-input", input, "-output", output, "-replay-store", replay, "-test-clock", now.Format(time.RFC3339)}
	args = append(args, trustedEvaluateArgs(t, tempDir)...)
	args = append(args, "evaluate")

	err := run(args)
	if err == nil || !strings.Contains(err.Error(), "event delivery id is required") {
		t.Fatalf("run() error = %v, want evaluation rejection", err)
	}

	written, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}

	var evaluation cigate.Evaluation
	if err := json.Unmarshal(written, &evaluation); err != nil {
		t.Fatal(err)
	}

	if evaluation.Check.Conclusion != "failure" {
		t.Fatalf("check conclusion = %s, want failure", evaluation.Check.Conclusion)
	}

	if !strings.Contains(evaluation.Check.Summary, "event delivery id is required") {
		t.Fatalf("check summary = %q, want delivery rejection", evaluation.Check.Summary)
	}
}

func TestEvaluateRejectsTrustedPolicyDigestMismatch(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "thrawn-evaluation.json")
	output := filepath.Join(tempDir, "thrawn-output.json")
	replay := filepath.Join(tempDir, "replay.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	if err := os.WriteFile(input, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-input", input, "-output", output, "-replay-store", replay, "-test-clock", now.Format(time.RFC3339)}
	args = append(args, trustedEvaluateArgsWithPolicyDigest(t, tempDir, digestHex("wrong-policy"))...)
	args = append(args, "evaluate")

	err := run(args)
	if err == nil || !strings.Contains(err.Error(), "expected policy digest") {
		t.Fatalf("run() error = %v, want trusted policy digest mismatch", err)
	}

	written, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}

	var evaluation cigate.Evaluation
	if err := json.Unmarshal(written, &evaluation); err != nil {
		t.Fatal(err)
	}

	if evaluation.Check.Conclusion != "failure" {
		t.Fatalf("check conclusion = %s, want failure", evaluation.Check.Conclusion)
	}
}

func TestEvaluateRejectsSelfConsistentTrustedPolicyForgery(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "thrawn-evaluation.json")
	replay := filepath.Join(tempDir, "replay.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	trusted := trustedManifest(t)
	forged := trusted
	forged.Modes[0].Coordinates[0].Trace.WorkflowPath = ".github/workflows/pr-controlled.yml"

	forgedData, err := forged.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	forgedPath := filepath.Join(tempDir, "forged-manifest.json")
	if err := os.WriteFile(forgedPath, forgedData, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(input, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-input", input, "-replay-store", replay, "-test-clock", now.Format(time.RFC3339)}
	args = append(args, trustedEvaluateArgsWithPolicy(t, tempDir, forgedPath, trusted.PolicyDigest)...)
	args = append(args, "evaluate")

	err = run(args)
	if err == nil || !strings.Contains(err.Error(), "expected policy digest") {
		t.Fatalf("run() error = %v, want self-consistent policy forgery rejection", err)
	}
}

func TestEvaluateRejectsTrustedConfigDigestMismatch(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "thrawn-evaluation.json")
	output := filepath.Join(tempDir, "thrawn-output.json")
	replay := filepath.Join(tempDir, "replay.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	if err := os.WriteFile(input, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(output, []byte(`{"check":{"conclusion":"success"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-input", input, "-output", output, "-replay-store", replay, "-test-clock", now.Format(time.RFC3339)}
	args = append(args, trustedEvaluateArgsWithConfigDigest(t, tempDir, digestHex("wrong-config"))...)
	args = append(args, "evaluate")

	err := run(args)
	if err == nil || !strings.Contains(err.Error(), "expected config digest") {
		t.Fatalf("run() error = %v, want trusted config digest mismatch", err)
	}

	written, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}

	var evaluation cigate.Evaluation
	if err := json.Unmarshal(written, &evaluation); err != nil {
		t.Fatal(err)
	}

	if evaluation.Check.Conclusion != "failure" {
		t.Fatalf("check conclusion = %s, want failure", evaluation.Check.Conclusion)
	}

	if !strings.Contains(evaluation.Check.Summary, "expected config digest") {
		t.Fatalf("check summary = %q, want config digest rejection", evaluation.Check.Summary)
	}
}

func TestEvaluateDecodeFailureOverwritesStaleOutput(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "dreich-evaluation.json")
	output := filepath.Join(tempDir, "dreich-output.json")
	replay := filepath.Join(tempDir, "replay.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	if err := os.WriteFile(input, []byte(`{"schema_version":1,"bogus_field":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(output, []byte(`{"check":{"conclusion":"success"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-input", input, "-output", output, "-replay-store", replay, "-test-clock", now.Format(time.RFC3339)}
	args = append(args, trustedEvaluateArgs(t, tempDir)...)
	args = append(args, "evaluate")

	err := run(args)
	if err == nil || !strings.Contains(err.Error(), "bogus_field") {
		t.Fatalf("run() error = %v, want strict decode rejection", err)
	}

	written, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}

	var evaluation cigate.Evaluation
	if err := json.Unmarshal(written, &evaluation); err != nil {
		t.Fatal(err)
	}

	if evaluation.Check.Conclusion != "failure" {
		t.Fatalf("check conclusion = %s, want failure", evaluation.Check.Conclusion)
	}

	if !strings.Contains(evaluation.Check.Summary, "bogus_field") {
		t.Fatalf("check summary = %q, want decode rejection", evaluation.Check.Summary)
	}
}

func TestRunRejectsTestClockWithoutExplicitTestGate(t *testing.T) {
	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "canny-evaluation.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	if err := os.WriteFile(input, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"-input", input, "-test-clock", now.Format(time.RFC3339), "evaluate"})
	if err == nil || !strings.Contains(err.Error(), "GRAITH_CIGATE_ALLOW_TEST_CLOCK=1") {
		t.Fatalf("run() error = %v, want test-clock gate rejection", err)
	}
}

func TestLiveProofWritesValidatedBundle(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "braw-live-proof.json")
	output := filepath.Join(tempDir, "canny-live-proof.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	bundle := liveProofBundle(now.Add(-time.Hour))

	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-input", input, "-output", output, "-test-clock", now.Format(time.RFC3339)}
	args = append(args, trustedConfigArgs(t, tempDir)...)
	args = append(args, "live-proof")

	if err := run(args); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}

	var got cigate.LiveProofBundle
	if err := json.Unmarshal(written, &got); err != nil {
		t.Fatal(err)
	}

	if got.Source != cigate.LiveProofSource {
		t.Fatalf("Source = %s, want %s", got.Source, cigate.LiveProofSource)
	}
}

func TestLiveProofRequiresTrustedConfig(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "bothy-live-proof.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	data, err := json.Marshal(liveProofBundle(now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = run([]string{"-input", input, "-test-clock", now.Format(time.RFC3339), "live-proof"})
	if err == nil || !strings.Contains(err.Error(), "-trusted-config is required") {
		t.Fatalf("run() error = %v, want trusted-config requirement", err)
	}
}

func TestLiveProofRequiresTrustedConfigDigest(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "bothy-live-proof.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	configPath, _ := writeTrustedConfig(t, tempDir, digestHex("release"), digestHex("evaluator"))

	data, err := json.Marshal(liveProofBundle(now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = run([]string{"-input", input, "-trusted-config", configPath, "-test-clock", now.Format(time.RFC3339), "live-proof"})
	if err == nil || !strings.Contains(err.Error(), "-config-digest is required") {
		t.Fatalf("run() error = %v, want config-digest requirement", err)
	}
}

func TestLiveProofRejectsTrustedConfigDigestMismatch(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "bothy-live-proof.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	configPath, _ := writeTrustedConfig(t, tempDir, digestHex("release"), digestHex("evaluator"))

	data, err := json.Marshal(liveProofBundle(now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = run([]string{
		"-input", input,
		"-trusted-config", configPath,
		"-config-digest", digestHex("wrong-config"),
		"-test-clock", now.Format(time.RFC3339),
		"live-proof",
	})
	if err == nil || !strings.Contains(err.Error(), "expected config digest") {
		t.Fatalf("run() error = %v, want config digest mismatch", err)
	}
}

func TestLiveProofRejectsEvaluateOnlyAnchors(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "bothy-live-proof.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	data, err := json.Marshal(liveProofBundle(now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-input", input, "-test-clock", now.Format(time.RFC3339)}
	args = append(args, trustedConfigArgs(t, tempDir)...)
	args = append(args, "-policy-digest", trustedManifest(t).PolicyDigest, "live-proof")

	err = run(args)
	if err == nil || !strings.Contains(err.Error(), "-policy-digest is only valid with evaluate") {
		t.Fatalf("run() error = %v, want evaluate-only anchor rejection", err)
	}
}

func TestLiveProofRejectsReplayStoreFlag(t *testing.T) {
	t.Setenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK", "1")

	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "dreich-live-proof.json")
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	data, err := json.Marshal(liveProofBundle(now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = run([]string{"-input", input, "-replay-store", filepath.Join(tempDir, "replay.json"), "-test-clock", now.Format(time.RFC3339), "live-proof"})
	if err == nil || !strings.Contains(err.Error(), "only valid with evaluate") {
		t.Fatalf("run() error = %v, want replay-store rejection", err)
	}
}

func trustedEvaluateArgs(t *testing.T, tempDir string) []string {
	t.Helper()

	manifest := trustedManifest(t)

	return trustedEvaluateArgsWithPolicyDigest(t, tempDir, manifest.PolicyDigest)
}

func trustedEvaluateArgsWithPolicyDigest(t *testing.T, tempDir, policyDigest string) []string {
	t.Helper()

	return trustedEvaluateArgsWithPolicy(t, tempDir, filepath.Join("..", "..", "internal", "cipolicy", "manifest.json"), policyDigest)
}

func trustedEvaluateArgsWithPolicy(t *testing.T, tempDir, policyPath, policyDigest string) []string {
	t.Helper()

	releaseDigest := digestHex("release")
	evaluatorDigest := digestHex("evaluator")

	return trustedEvaluateArgsWithConfigAndPolicy(t, tempDir, releaseDigest, evaluatorDigest, "", policyPath, policyDigest)
}

func trustedEvaluateArgsWithConfigDigest(t *testing.T, tempDir, configDigest string) []string {
	t.Helper()

	manifest := trustedManifest(t)

	return trustedEvaluateArgsWithConfigAndPolicy(
		t,
		tempDir,
		digestHex("release"),
		digestHex("evaluator"),
		configDigest,
		filepath.Join("..", "..", "internal", "cipolicy", "manifest.json"),
		manifest.PolicyDigest,
	)
}

func trustedEvaluateArgsWithConfigAndPolicy(t *testing.T, tempDir, releaseDigest, evaluatorDigest, configDigest, policyPath, policyDigest string) []string {
	t.Helper()

	configPath, actualConfigDigest := writeTrustedConfig(t, tempDir, releaseDigest, evaluatorDigest)
	if configDigest == "" {
		configDigest = actualConfigDigest
	}

	return []string{
		"-trusted-policy", policyPath,
		"-policy-digest", policyDigest,
		"-trusted-config", configPath,
		"-config-digest", configDigest,
		"-evaluator-version", "v0.0.0-test",
		"-release-digest", releaseDigest,
		"-evaluator-digest", evaluatorDigest,
	}
}

func trustedConfigArgs(t *testing.T, tempDir string) []string {
	t.Helper()

	configPath, configDigest := writeTrustedConfig(t, tempDir, digestHex("release"), digestHex("evaluator"))

	return []string{"-trusted-config", configPath, "-config-digest", configDigest}
}

func trustedManifest(t *testing.T) cipolicy.Manifest {
	t.Helper()

	manifest, err := cipolicy.ReadManifest(filepath.Join("..", "..", "internal", "cipolicy", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}

	return manifest
}

func writeTrustedConfig(t *testing.T, tempDir, releaseDigest, evaluatorDigest string) (string, string) {
	t.Helper()

	config := cigate.Config{
		SchemaVersion: cigate.SchemaVersion,
		Repository:    cipolicy.DefaultRepository,
		DefaultBranch: cipolicy.DefaultDefaultBranch,
		App: cigate.AppContract{
			Slug:              cigate.CheckName,
			ID:                424242,
			Owner:             "graith-maintainers",
			InstallationOwner: "d0ugal",
			CheckName:         cigate.CheckName,
			Permissions: map[string]string{
				"metadata":      "read",
				"contents":      "read",
				"actions":       "read",
				"pull_requests": "read",
				"checks":        "write",
			},
			Events: []string{"pull_request", "merge_group"},
		},
		Deployment: cigate.DeploymentContract{
			Runtime:         "fixture-hosted-runtime",
			ReleaseDigest:   releaseDigest,
			EvaluatorDigest: evaluatorDigest,
			AttestationKey: cigate.AttestationKey{
				Service:    "fixture-attestation-kms",
				KeyID:      "projects/braw/locations/global/keyRings/canny/cryptoKeys/gate",
				TrustModel: "reviewed-release-digest-signed-by-maintainer-key",
			},
			Rotation: cigate.RotationContract{
				Owner:   "graith-maintainers",
				Cadence: "90d",
				Runbook: "docs/runbooks/ci-gate-rotation.md",
			},
			IncidentRevocation: cigate.RevocationContract{
				Owner:   "graith-maintainers",
				Runbook: "docs/runbooks/ci-gate-revoke.md",
			},
		},
		Retention: cigate.RetentionContract{
			Owner:    "graith-maintainers",
			Location: "artifact-store://braw/graith-ci-gate",
			Duration: "2160h",
		},
		LiveProof: cigate.LiveProofContract{
			FixtureRepository: "d0ugal/graith-ci-gate-fixture",
		},
		Operators: []string{"graith-maintainers"},
	}

	data, err := cigate.EncodeCanonical(config)
	if err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(tempDir, "trusted-config.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	return configPath, digestRaw(data)
}

func liveProofBundle(collectedAt time.Time) cigate.LiveProofBundle {
	requiredCases := cigate.RequiredLiveProofCases()

	cases := make([]cigate.LiveProofCase, 0, len(requiredCases))
	for _, id := range requiredCases {
		cases = append(cases, cigate.LiveProofCase{
			ID:              id,
			Status:          "passed",
			EventDeliveryID: "delivery-" + id,
			HeadSHA:         strings.Repeat("1", 40),
			BaseSHA:         strings.Repeat("2", 40),
			ArtifactDigest:  digestHex("artifact", id),
			RequiredCheckID: "check-" + id,
			EvidenceURI:     "artifact-store://bothy/live/" + id,
		})
	}

	return cigate.LiveProofBundle{
		SchemaVersion:     cigate.SchemaVersion,
		Source:            cigate.LiveProofSource,
		FixtureRepository: "d0ugal/graith-ci-gate-fixture",
		AppID:             424242,
		AppSlug:           cigate.CheckName,
		RulesetCheckAppID: 424242,
		NoBypassActors:    true,
		MergeQueueEnabled: true,
		CollectedAt:       collectedAt,
		Cases:             cases,
		ExternalDecisions: cigate.ExternalDecisions{
			Runtime:               "fixture-hosted-runtime",
			AttestationKeyService: "fixture-attestation-kms",
			AttestationKeyID:      "projects/braw/locations/global/keyRings/canny/cryptoKeys/gate",
			TrustModel:            "reviewed-release-digest-signed-by-maintainer-key",
			RotationOwner:         "graith-maintainers",
			RetentionOwner:        "graith-maintainers",
			RevocationOwner:       "graith-maintainers",
			OperatorOwner:         "graith-maintainers",
		},
	}
}

func digestHex(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func digestRaw(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
