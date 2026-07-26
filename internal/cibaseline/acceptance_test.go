package cibaseline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcceptanceManifestValidation(t *testing.T) {
	inventory := loadInventory(t)
	replacementPath := "retained/p0-20260725T084200Z-20260725T144200Z/acceptance.json"
	replacement := readAcceptanceManifestForTest(t, replacementPath)

	if err := ValidateAcceptanceManifest(replacement, inventory, AcceptanceValidationOptions{
		AllowIncomplete: true,
		ManifestPath:    replacementPath,
	}); err != nil {
		t.Fatalf("ValidateAcceptanceManifest(replacement package) = %v, want nil", err)
	}

	if err := ValidateAcceptanceManifest(replacement, inventory, AcceptanceValidationOptions{AllowIncomplete: true}); err == nil ||
		!strings.Contains(err.Error(), "complete acceptance requires manifest path") {
		t.Fatalf("ValidateAcceptanceManifest(complete without path) = %v, want manifest path rejection", err)
	}

	incomplete := signedIncompleteAcceptanceManifest(t, inventory)
	if err := ValidateAcceptanceManifest(incomplete, inventory, AcceptanceValidationOptions{AllowIncomplete: true}); err != nil {
		t.Fatalf("ValidateAcceptanceManifest(incomplete structure) = %v, want nil", err)
	}

	if err := ValidateAcceptanceManifest(incomplete, inventory, AcceptanceValidationOptions{}); err == nil ||
		!strings.Contains(err.Error(), "p0 acceptance unsatisfied") {
		t.Fatalf("ValidateAcceptanceManifest(incomplete gate) = %v, want strict rejection", err)
	}
}

func TestAcceptanceManifestRejectsClosedWorldFailures(t *testing.T) {
	const manifestPath = "retained/p0-20260725T084200Z-20260725T144200Z/acceptance.json"

	inventory := loadInventory(t)

	tests := map[string]struct {
		mutate func(*AcceptanceManifest)
		want   string
	}{
		"changed bounds": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.CollectionRequest.RequestedUntil = manifest.CollectionRequest.RequestedUntil.Add(time.Second)
			},
			want: "unexpected collection request",
		},
		"missing matrix row": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.ModeMatrix = manifest.ModeMatrix[1:]
			},
			want: "missing mode matrix rows",
		},
		"missing inventory rebind": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.InventoryRebind = nil
			},
			want: "inventory rebind metadata is required",
		},
		"wrong inventory rebind surface": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.InventoryRebind.ChangedPolicySurfaces[0].OldSHA256 = strings.Repeat("a", 64)
			},
			want: "inventory rebind policy surface delta",
		},
		"wrong inventory rebind target": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.InventoryRebind.ToDigest = strings.Repeat("b", 64)
			},
			want: "unexpected digest endpoints",
		},
		"wrong inventory rebind matrix": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.InventoryRebind.ModeMatrixDigest = strings.Repeat("c", 64)
			},
			want: "inventory rebind proof",
		},
		"duplicate matrix row": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.ModeMatrix = append(manifest.ModeMatrix, manifest.ModeMatrix[0])
			},
			want: "duplicate mode matrix row",
		},
		"orphan observed mode": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.ObservedCells[0].ModeCoordinate = "ci/thrawn"
			},
			want: "unknown mode",
		},
		"expired no-target row": {
			mutate: func(manifest *AcceptanceManifest) {
				for index := range manifest.LatencyPolicies {
					if manifest.LatencyPolicies[index].Policy == "no-latency-target" {
						manifest.LatencyPolicies[index].ExpiresOn = "2026-07-24"

						return
					}
				}
			},
			want: "expired before collection end",
		},
		"missing matched minutes": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.ObservedCells[0].MatchedRunnerMinutes = false
				manifest.ObservedCells[0].RunnerMinutes = nil
			},
			want: "missing matched runner-minute",
		},
		"observed event shape drift": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.ObservedCells[0].EventShape = "push/main"
			},
			want: "does not match change classification",
		},
		"missing change classification": {
			mutate: func(manifest *AcceptanceManifest) {
				cell := manifest.ObservedCells[0]
				key := classificationKey(cell.ChangeID, cell.Event, cell.Ref)

				filtered := manifest.ChangeClassifications[:0]
				for _, row := range manifest.ChangeClassifications {
					if classificationKey(row.ChangeID, row.Event, row.Ref) != key {
						filtered = append(filtered, row)
					}
				}

				manifest.ChangeClassifications = filtered
			},
			want: "has no change classification",
		},
		"missing mode coverage gap": {
			mutate: func(manifest *AcceptanceManifest) {
				filtered := manifest.GapRows[:0]
				for _, row := range manifest.GapRows {
					if row.Classification != "mode-not-exercised" {
						filtered = append(filtered, row)
					}
				}

				manifest.GapRows = filtered
				manifest.EvidencePackage.GapRowCount = len(filtered)
			},
			want: "uncovered mode matrix rows",
		},
		"mode gap dual-run eligible": {
			mutate: func(manifest *AcceptanceManifest) {
				for index := range manifest.GapRows {
					if manifest.GapRows[index].Classification == "mode-not-exercised" {
						manifest.GapRows[index].DualRunEligible = true

						return
					}
				}
			},
			want: "mode-not-exercised gap",
		},
		"mode gap wrong owner": {
			mutate: func(manifest *AcceptanceManifest) {
				for index := range manifest.GapRows {
					if manifest.GapRows[index].Classification == "mode-not-exercised" {
						manifest.GapRows[index].Owner = "graith-maintainers"
						if strings.HasPrefix(manifest.GapRows[index].ModeCoordinate, "workflow-lint/") {
							manifest.GapRows[index].Owner = "native-owners"
						}

						return
					}
				}
			},
			want: "mode-not-exercised gap",
		},
		"duplicate retained observed cell": {
			mutate: func(manifest *AcceptanceManifest) {
				duplicate := manifest.ObservedCells[0]
				duplicate.ID += ":duplicate"
				manifest.ObservedCells = append(manifest.ObservedCells, duplicate)
				manifest.EvidencePackage.ObservedCellCount = len(manifest.ObservedCells)
				manifest.SignOff.ObservedCellCount = manifest.EvidencePackage.ObservedCellCount
			},
			want: "reference retained observation",
		},
		"unapproved event shape mismatch": {
			mutate: func(manifest *AcceptanceManifest) {
				var changed ChangeClassificationRow

				for index := range manifest.ChangeClassifications {
					if manifest.ChangeClassifications[index].Event == "pull_request" &&
						strings.HasPrefix(manifest.ChangeClassifications[index].EventShape, "pull_request/") {
						manifest.ChangeClassifications[index].EventShape = "push/release-candidate"
						manifest.ChangeClassifications[index].LatencyPolicyID = "latency-release-candidate"
						changed = manifest.ChangeClassifications[index]

						break
					}
				}

				for index := range manifest.ObservedCells {
					if manifest.ObservedCells[index].ChangeID == changed.ChangeID &&
						manifest.ObservedCells[index].Event == changed.Event &&
						manifest.ObservedCells[index].Ref == changed.Ref {
						manifest.ObservedCells[index].EventShape = changed.EventShape
						manifest.ObservedCells[index].LatencyPolicyID = changed.LatencyPolicyID
					}
				}
			},
			want: "does not match event",
		},
		"ceiling exhaustion": {
			mutate: func(manifest *AcceptanceManifest) {
				minutes := int64(1001)
				manifest.EvidencePackage.CurrentGraphRunnerMinutes = &minutes
			},
			want: "exceeds ceiling",
		},
		"non-representative replay": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.RepresentativeReplay.Changes[0].ModeCoordinates = []string{"ci/thrawn"}
			},
			want: "unexplained mode",
		},
		"changed digest": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.Digest = strings.Repeat("0", 64)
			},
			want: "digest mismatch",
		},
		"location drift": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.EvidencePackage.Location = "retained"
			},
			want: "location does not match",
		},
		"stale signoff inventory binding": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.SignOff.InventoryDigest = p0CollectedInventoryDigest
			},
			want: "sign-off is not bound to retained evidence",
		},
		"stale signoff reviewed manifest digest": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.SignOff.ReviewedManifestDigest = strings.Repeat("e", 64)
			},
			want: "sign-off is not bound to retained evidence",
		},
		"stale signoff approval source": {
			mutate: func(manifest *AcceptanceManifest) {
				manifest.SignOff.ApprovalSource = "graith message msg_eea054ac26657074"
			},
			want: "sign-off is not bound to retained evidence",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := readAcceptanceManifestForTest(t, manifestPath)
			markAcceptanceSatisfiedForTest(t, &manifest)
			test.mutate(&manifest)

			if name == "duplicate retained observed cell" {
				rebindSignOffForTest(t, &manifest)
			}

			if name != "changed digest" {
				manifest = finalizeAcceptanceForTest(t, manifest)
			}

			if err := ValidateAcceptanceManifest(manifest, inventory, AcceptanceValidationOptions{
				AllowIncomplete: true,
				ManifestPath:    manifestPath,
			}); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateAcceptanceManifest() = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestAcceptanceManifestRejectsCurrentGraphMinuteDrift(t *testing.T) {
	const manifestPath = "retained/p0-20260725T084200Z-20260725T144200Z/acceptance.json"

	inventory := loadInventory(t)
	manifest := readAcceptanceManifestForTest(t, manifestPath)
	minutes := int64(610)
	manifest.EvidencePackage.CurrentGraphRunnerMinutes = &minutes
	rebindSignOffForTest(t, &manifest)
	manifest = finalizeAcceptanceForTest(t, manifest)

	err := ValidateAcceptanceManifest(manifest, inventory, AcceptanceValidationOptions{
		AllowIncomplete: true,
		ManifestPath:    manifestPath,
	})
	if err == nil || !strings.Contains(err.Error(), "current graph runner minutes") {
		t.Fatalf("ValidateAcceptanceManifest() = %v, want current graph minute rejection", err)
	}
}

func TestRetainedModeCoverageRejectsSyntheticCancelledOnly(t *testing.T) {
	coordinate := "goreleaser/execute-linux[goarch=amd64,runner=ubuntu-24.04]"
	observation := RunEvidence{
		Coordinate:      coordinate,
		SyntheticFanout: true,
		JobStatus:       "completed",
		JobConclusion:   "cancelled",
		Outcome:         "cancelled",
		RunDisposition:  "superseded",
		ExecutionMillis: 0,
	}

	if retainedObservationCoversMode(observation) {
		t.Fatal("cancelled synthetic fanout covered a mode")
	}

	manifest := AcceptanceManifest{
		ModeMatrix: []ModeMatrixRow{{ModeCoordinate: coordinate}},
		Result:     AcceptanceResult{P0ExitSatisfied: true},
	}

	err := validateRetainedModeCoverage(manifest, map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "no retained coverage-eligible observation") {
		t.Fatalf("validateRetainedModeCoverage() = %v, want cancelled-fanout coverage rejection", err)
	}

	skippedObservation := observation
	skippedObservation.JobConclusion = "skipped"
	skippedObservation.Outcome = "skipped"

	if !retainedObservationCoversMode(skippedObservation) {
		t.Fatal("skipped synthetic fanout did not cover a mode")
	}
}

func TestAcceptanceManifestRejectsUnapprovedGapRows(t *testing.T) {
	inventory := loadInventory(t)
	manifest := signedIncompleteAcceptanceManifest(t, inventory)
	manifest.GapRows[1].RunID = 30151461868
	manifest = finalizeAcceptanceForTest(t, manifest)

	if err := ValidateAcceptanceManifest(manifest, inventory, AcceptanceValidationOptions{AllowIncomplete: true}); err == nil ||
		!strings.Contains(err.Error(), "does not match approved provider timestamp anomaly") {
		t.Fatalf("ValidateAcceptanceManifest() = %v, want timestamp gap rejection", err)
	}
}

func TestCommittedP0AcceptanceManifests(t *testing.T) {
	inventory := loadInventory(t)

	original := readAcceptanceManifestForTest(t, "retained/p0-20260725T060500Z-20260725T120500Z/incomplete-acceptance.json")
	if err := ValidateAcceptanceManifest(original, inventory, AcceptanceValidationOptions{
		AllowIncomplete: true,
		ManifestPath:    "retained/p0-20260725T060500Z-20260725T120500Z/incomplete-acceptance.json",
	}); err != nil {
		t.Fatalf("original incomplete manifest structure = %v", err)
	}

	if err := ValidateAcceptanceManifest(original, inventory, AcceptanceValidationOptions{
		ManifestPath: "retained/p0-20260725T060500Z-20260725T120500Z/incomplete-acceptance.json",
	}); err == nil ||
		!strings.Contains(err.Error(), "provider timestamp anomaly") {
		t.Fatalf("original strict acceptance = %v, want timestamp-anomaly rejection", err)
	}

	replacement := readAcceptanceManifestForTest(t, "retained/p0-20260725T084200Z-20260725T144200Z/acceptance.json")
	if err := ValidateAcceptanceManifest(replacement, inventory, AcceptanceValidationOptions{
		AllowIncomplete: true,
		ManifestPath:    "retained/p0-20260725T084200Z-20260725T144200Z/acceptance.json",
	}); err != nil {
		t.Fatalf("replacement pending manifest structure = %v", err)
	}

	err := ValidateAcceptanceManifest(replacement, inventory, AcceptanceValidationOptions{
		ManifestPath: "retained/p0-20260725T084200Z-20260725T144200Z/acceptance.json",
	})
	if replacement.Result.P0ExitSatisfied {
		if err != nil {
			t.Fatalf("replacement strict acceptance = %v, want nil", err)
		}
	} else if err == nil || !strings.Contains(err.Error(), "unsatisfied-owner-signoff") {
		t.Fatalf("replacement strict acceptance = %v, want pending sign-off rejection", err)
	}

	data, err := os.ReadFile("retained/p0-20260725T084200Z-20260725T144200Z/repo-owned-evidence-552b0e007cc1cc861d93773b72588fb46350f2a7549fc8787ba22eccfa4aa82c.json")
	if err != nil {
		t.Fatal(err)
	}

	var evidence Evidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}

	if err := evidence.Replay(inventory); err != nil {
		t.Fatalf("replacement repo-owned evidence replay = %v", err)
	}
}

func TestAcceptanceManifestRejectsRetainedEvidenceTampering(t *testing.T) {
	inventory := loadInventory(t)
	manifestPath := "retained/p0-20260725T084200Z-20260725T144200Z/acceptance.json"

	t.Run("path escape", func(t *testing.T) {
		manifest := readAcceptanceManifestForTest(t, manifestPath)
		manifest.EvidencePackage.Location = ".."
		manifest.EvidencePackage.WindowBundlePath = "../" + manifest.EvidencePackage.WindowBundlePath
		manifest.EvidencePackage.RepoEvidencePath = "../" + manifest.EvidencePackage.RepoEvidencePath
		rebindSignOffForTest(t, &manifest)
		manifest = finalizeAcceptanceForTest(t, manifest)

		err := ValidateAcceptanceManifest(manifest, inventory, AcceptanceValidationOptions{
			AllowIncomplete: true,
			ManifestPath:    manifestPath,
		})
		if err == nil || !strings.Contains(err.Error(), "escapes the manifest directory") {
			t.Fatalf("ValidateAcceptanceManifest() = %v, want path escape rejection", err)
		}
	})

	t.Run("repo digest", func(t *testing.T) {
		manifest := readAcceptanceManifestForTest(t, manifestPath)
		manifest.EvidencePackage.RepoEvidenceDigest = strings.Repeat("d", 64)
		rebindSignOffForTest(t, &manifest)
		manifest = finalizeAcceptanceForTest(t, manifest)

		err := ValidateAcceptanceManifest(manifest, inventory, AcceptanceValidationOptions{
			AllowIncomplete: true,
			ManifestPath:    manifestPath,
		})
		if err == nil || !strings.Contains(err.Error(), "repo evidence digest mismatch") {
			t.Fatalf("ValidateAcceptanceManifest() = %v, want repo digest rejection", err)
		}
	})

	t.Run("bundle bounds", func(t *testing.T) {
		manifest, bundle, evidence := readCommittedReplacementPackage(t)
		bundle.RequestedUntil = bundle.RequestedUntil.Add(time.Second)
		resignBundle(t, &bundle)

		path := writeRetainedPackageForTest(t, manifest, bundle, evidence)

		err := ValidateAcceptanceManifest(readAcceptanceManifestForTest(t, path), inventory, AcceptanceValidationOptions{
			AllowIncomplete: true,
			ManifestPath:    path,
		})
		if err == nil || !strings.Contains(err.Error(), "window bundle identity") {
			t.Fatalf("ValidateAcceptanceManifest() = %v, want bundle bounds rejection", err)
		}
	})

	t.Run("repo inventory", func(t *testing.T) {
		manifest, bundle, evidence := readCommittedReplacementPackage(t)
		evidence.InventoryDigest = strings.Repeat("e", 64)
		resignEvidence(t, &evidence)
		bundle.Evidence = evidence
		bundle.EvidenceDigest = evidence.Digest
		resignBundle(t, &bundle)

		path := writeRetainedPackageForTest(t, manifest, bundle, evidence)

		err := ValidateAcceptanceManifest(readAcceptanceManifestForTest(t, path), inventory, AcceptanceValidationOptions{
			AllowIncomplete: true,
			ManifestPath:    path,
		})
		if err == nil || !strings.Contains(err.Error(), "inventory digest mismatch") {
			t.Fatalf("ValidateAcceptanceManifest() = %v, want repo inventory rejection", err)
		}
	})

	t.Run("bundle rebind mismatch", func(t *testing.T) {
		manifest, bundle, evidence := readCommittedReplacementPackage(t)
		bundle.InventoryRebind.Source = "dreich"
		resignBundle(t, &bundle)

		path := writeRetainedPackageForTest(t, manifest, bundle, evidence)

		err := ValidateAcceptanceManifest(readAcceptanceManifestForTest(t, path), inventory, AcceptanceValidationOptions{
			AllowIncomplete: true,
			ManifestPath:    path,
		})
		if err == nil || !strings.Contains(err.Error(), "window bundle identity") {
			t.Fatalf("ValidateAcceptanceManifest() = %v, want bundle rebind rejection", err)
		}
	})

	t.Run("external timing map missing", func(t *testing.T) {
		manifest, bundle, evidence := readCommittedReplacementPackage(t)
		external := &bundle.ExternalRuns[0]
		external.Cost.BillableMinutes = map[string]int64{}
		external.TimingBillableMillis = map[string]int64{}
		digest := digestExternalRunForTest(t, *external)
		bundle.ExternalRunDigests[0] = digest
		manifest.EvidencePackage.ExternalRunDigests[0] = digest

		resignBundle(t, &bundle)

		path := writeRetainedPackageForTest(t, manifest, bundle, evidence)

		err := ValidateAcceptanceManifest(readAcceptanceManifestForTest(t, path), inventory, AcceptanceValidationOptions{
			AllowIncomplete: true,
			ManifestPath:    path,
		})
		if err == nil || !strings.Contains(err.Error(), "approved Dependabot evidence") {
			t.Fatalf("ValidateAcceptanceManifest() = %v, want external timing-map rejection", err)
		}
	})
}

func readAcceptanceManifestForTest(t *testing.T, path string) AcceptanceManifest {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var manifest AcceptanceManifest

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}

	return manifest
}

func readCommittedReplacementPackage(t *testing.T) (AcceptanceManifest, P0WindowEvidenceBundle, Evidence) {
	t.Helper()

	const manifestPath = "retained/p0-20260725T084200Z-20260725T144200Z/acceptance.json"

	manifest := readAcceptanceManifestForTest(t, manifestPath)
	base := filepath.Dir(manifestPath)

	var bundle P0WindowEvidenceBundle

	data, err := os.ReadFile(filepath.Join(base, manifest.EvidencePackage.WindowBundlePath))
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}

	var evidence Evidence

	data, err = os.ReadFile(filepath.Join(base, manifest.EvidencePackage.RepoEvidencePath))
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}

	return manifest, bundle, evidence
}

func writeRetainedPackageForTest(t *testing.T, manifest AcceptanceManifest, bundle P0WindowEvidenceBundle, evidence Evidence) string {
	t.Helper()

	dir := t.TempDir()
	bundleName := "window-evidence-" + bundle.Digest + ".json"
	evidenceName := "repo-owned-evidence-" + evidence.Digest + ".json"

	manifest.EvidencePackage.Location = "."
	manifest.EvidencePackage.WindowBundlePath = bundleName
	manifest.EvidencePackage.WindowBundleDigest = bundle.Digest
	manifest.EvidencePackage.RepoEvidencePath = evidenceName
	manifest.EvidencePackage.RepoEvidenceDigest = evidence.Digest
	rebindSignOffForTest(t, &manifest)
	manifest = finalizeAcceptanceForTest(t, manifest)

	writeJSONForTest(t, filepath.Join(dir, bundleName), bundle)
	writeJSONForTest(t, filepath.Join(dir, evidenceName), evidence)
	manifestPath := filepath.Join(dir, "acceptance.json")
	writeJSONForTest(t, manifestPath, manifest)

	return manifestPath
}

func resignBundle(t *testing.T, bundle *P0WindowEvidenceBundle) {
	t.Helper()

	bundle.Digest = ""

	digest, err := p0LogicalDigest(*bundle)
	if err != nil {
		t.Fatal(err)
	}

	bundle.Digest = digest
}

func digestExternalRunForTest(t *testing.T, external GitHubExternalRunEvidence) string {
	t.Helper()

	digest, err := p0LogicalDigest(external)
	if err != nil {
		t.Fatal(err)
	}

	return digest
}

func writeJSONForTest(t *testing.T, path string, value any) {
	t.Helper()

	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func signedIncompleteAcceptanceManifest(t *testing.T, inventory Inventory) AcceptanceManifest {
	t.Helper()

	modeMatrix, err := BuildModeMatrix(inventory)
	if err != nil {
		t.Fatal(err)
	}

	manifest := AcceptanceManifest{
		SchemaVersion: AcceptanceSchemaVersion,
		CollectionRequest: CollectionRequestManifest{
			ID: "p0-2026-07-25T060500Z-2026-07-25T120500Z", Owner: "graith-maintainers",
			ApprovedBy: "ci-north-star-rollout", ApprovalSource: "task prompt and graith message msg_c11444aa1a18d615",
			RequestedSince:  time.Date(2026, 7, 25, 6, 5, 0, 0, time.UTC),
			RequestedUntil:  time.Date(2026, 7, 25, 12, 5, 0, 0, time.UTC),
			FixedContiguous: true, AbsoluteCeilingRunnerMinutes: 1000,
		},
		InventoryDigest: inventory.Digest,
		InventoryRebind: inventoryRebindForTest(t, inventory, digestJSON(modeMatrix)),
		LatencyPolicies: acceptanceLatencyPolicies(inventory),
		ModeMatrix:      modeMatrix,
		ChangeClassifications: []ChangeClassificationRow{
			{
				ID: "classification:external-30152132020", ChangeID: "63f89267ebd0a858e22782416e2905fa2fcd43b8",
				Event: "dynamic", Ref: "main", EventShape: "dynamic/dependabot/update-graph",
				LatencyPolicyID: "no-target-dynamic-dependabot-update-graph", Owner: "graith-maintainers",
				Source: "synthetic fixture", Basis: "GitHub-generated Dependabot dependency graph update",
			},
			{
				ID: "classification:timestamp-anomaly-30151461867", ChangeID: "697c4c5d53721e3eccbd33008b77c51e6c4bc809",
				Event: "pull_request", Ref: "d0ugal/graith/issue-1637-upgrade-recovery-b0466102",
				EventShape: "push/release-candidate", LatencyPolicyID: "latency-release-candidate",
				Owner: "graith-maintainers", Source: "synthetic fixture", Basis: "approved timestamp anomaly retains release-candidate target",
				Files: []string{"gui/shared/Sources/GraithProtocol/Messages.swift"},
			},
		},
		ObservedCells: []ObservedCellRow{
			{
				ID: "original:external:30152132020", ChangeID: "63f89267ebd0a858e22782416e2905fa2fcd43b8",
				Event: "dynamic", Ref: "main", EventShape: "dynamic/dependabot/update-graph", LatencyPolicyID: "no-target-dynamic-dependabot-update-graph",
				EvidenceState: "gap", MatchedRunnerMinutes: true, RunnerMinutes: int64Ptr(1),
				RunnerMinutesSource:     "retained-external-job-execution-duration",
				RunnerMinutesDerivation: "ceil(24000/60000)",
				RunID:                   30152132020, RunAttempt: 1, JobID: 89664191125, GapRowID: "gap-external-dependabot-update-graph-30152132020",
			},
			{
				ID: "original:goreleaser/release-context:30151461867", ChangeID: "697c4c5d53721e3eccbd33008b77c51e6c4bc809",
				Event: "pull_request", Ref: "d0ugal/graith/issue-1637-upgrade-recovery-b0466102", EventShape: "push/release-candidate", WorkflowID: "goreleaser",
				WorkflowPath: ".github/workflows/goreleaser.yml", ModeCoordinate: "goreleaser/release-context",
				LatencyPolicyID: "latency-release-candidate", EvidenceState: "anomaly", MatchedRunnerMinutes: false,
				RunID: 30151461867, RunAttempt: 1, JobID: 89662641145, GapRowID: "gap-provider-timestamp-anomaly-30151461867-89662641145",
			},
		},
		GapRows: []AcceptanceGapRow{externalGapRow(), timestampAnomalyGapRow()},
		EvidencePackage: EvidencePackageManifest{
			Location:     "internal/cibaseline/retained/p0-20260725T060500Z-20260725T120500Z/incomplete-acceptance.json",
			ManifestKind: "p0-acceptance", ModeMatrixDigest: digestJSON(modeMatrix),
			ObservedCellCount: 2, GapRowCount: 2, MeasurementState: "incomplete",
			MeasurementIncompleteReason: "no matched runner-minute observation exists for goreleaser/release-context on run 30151461867 job 89662641145",
		},
		RepresentativeReplay: RepresentativeReplayManifest{
			Status: "blocked", ObservedModeSet: "original-request-incomplete",
			BlockedReason: "provider timestamp anomaly blocks the goreleaser/release-context cell",
		},
		Result: AcceptanceResult{
			P0ExitSatisfied: false,
			UnsatisfiedCells: []string{
				"pull_request:697c4c5d53721e3eccbd33008b77c51e6c4bc809:goreleaser/release-context",
			},
			Reasons: []string{"provider timestamp anomaly lacks a matched runner-minute observation for goreleaser/release-context"},
		},
	}

	return finalizeAcceptanceForTest(t, manifest)
}

func inventoryRebindForTest(t *testing.T, inventory Inventory, modeMatrixDigest string) *InventoryRebindManifest {
	t.Helper()

	if inventory.Digest == p0CollectedInventoryDigest {
		return nil
	}

	for _, surface := range inventory.Surfaces {
		if surface.Path == "Makefile" {
			return &InventoryRebindManifest{
				FromDigest:            p0CollectedInventoryDigest,
				ToDigest:              inventory.Digest,
				Source:                p0InventoryRebindSource,
				Derivation:            p0InventoryRebindDerivation,
				WorkflowDelta:         "none",
				LegacyMappingDelta:    "none",
				RequiredContextsDelta: "none",
				ModeMatrixDelta:       "none",
				ModeMatrixDigest:      modeMatrixDigest,
				EvidenceEffect:        "offline-replay-only; no workflow/event/permission/coordinate/required-context claim changed",
				ChangedPolicySurfaces: []InventorySurfaceDelta{
					{
						Path: surface.Path, Owner: surface.Owner, Kind: surface.Kind, GitMode: surface.GitMode,
						OldSHA256: p0OldMakefileSHA256, NewSHA256: surface.SHA256, Contract: surface.Contract,
						Disposition: surface.Disposition, Retirement: surface.Retirement,
					},
				},
				OwnerReviewRequired:     true,
				DualRunSamplingEligible: false,
			}
		}
	}

	t.Fatal("current inventory has no Makefile policy surface")

	return nil
}

func acceptanceLatencyPolicies(inventory Inventory) []LatencyPolicyRow {
	rows := []LatencyPolicyRow{
		{
			ID: "latency-go-only-pr", EventShape: "pull_request/go-only", Policy: "target", TargetMinutes: 20,
			Owner: "graith-maintainers", ApprovalSource: "task prompt", PreCollectionApproved: true,
			Rationale: "owner-approved provisional Go-only PR latency ceiling",
		},
		{
			ID: "latency-gui-native-pr", EventShape: "pull_request/gui-or-native-touching", Policy: "target", TargetMinutes: 35,
			Owner: "graith-maintainers", ApprovalSource: "task prompt", PreCollectionApproved: true,
			Rationale: "owner-approved provisional GUI/native-touching PR latency ceiling",
		},
		{
			ID: "latency-main", EventShape: "push/main", Policy: "target", TargetMinutes: 45,
			Owner: "graith-maintainers", ApprovalSource: "task prompt", PreCollectionApproved: true,
			Rationale: "owner-approved provisional main latency ceiling",
		},
		{
			ID: "latency-release-candidate", EventShape: "push/release-candidate", Policy: "target", TargetMinutes: 90,
			Owner: "graith-maintainers", ApprovalSource: "task prompt", PreCollectionApproved: true,
			Rationale: "owner-approved provisional release candidate latency ceiling",
		},
		{
			ID: "no-target-push-release-please-branch", EventShape: "push/release-please-branch",
			Policy: "no-latency-target", Owner: "graith-maintainers", ApprovalSource: "task prompt",
			PreCollectionApproved: true, ExpiresOn: "2026-08-31",
			Rationale: "release-please maintenance branch push has no provisional latency SLO",
		},
		{
			ID: "no-target-dynamic-dependabot-update-graph", EventShape: "dynamic/dependabot/update-graph",
			Policy: "no-latency-target", Owner: "graith-maintainers", ApprovalSource: "graith message msg_c11444aa1a18d615",
			PreCollectionApproved: true, ExpiresOn: "2026-08-31",
			Rationale: "GitHub-generated Dependabot dependency-graph update outside the 18 repo-owned workflow paths",
		},
	}

	for _, workflow := range inventory.Workflows {
		events := string(workflow.Events)
		if strings.Contains(events, `"workflow_dispatch"`) {
			rows = append(rows, noTargetPolicy("workflow_dispatch/"+workflow.ID, "manual dispatch baseline event"))
		}

		if strings.Contains(events, `"schedule"`) {
			rows = append(rows, noTargetPolicy("schedule/"+workflow.ID, "scheduled baseline event"))
		}
	}

	return rows
}

func noTargetPolicy(eventShape, rationale string) LatencyPolicyRow {
	return LatencyPolicyRow{
		ID: "no-target-" + strings.ReplaceAll(eventShape, "/", "-"), EventShape: eventShape,
		Policy: "no-latency-target", Owner: "graith-maintainers", ApprovalSource: "task prompt",
		PreCollectionApproved: true, ExpiresOn: "2026-08-31", Rationale: rationale,
	}
}

func externalGapRow() AcceptanceGapRow {
	zero := int64(0)
	one := int64(1)

	return AcceptanceGapRow{
		ID: "gap-external-dependabot-update-graph-30152132020", Classification: "external-workflow",
		Owner: "graith-maintainers", ApprovalSource: "graith message msg_c11444aa1a18d615", ExpiresOn: "2026-08-31",
		Rationale:    "GitHub-generated Dependabot dependency-graph update outside the 18 repo-owned workflow paths",
		WorkflowPath: "dynamic/dependabot/update-graph", Event: "dynamic", Ref: "main", EventShape: "dynamic/dependabot/update-graph",
		ChangeID: "63f89267ebd0a858e22782416e2905fa2fcd43b8", LatencyPolicyID: "no-target-dynamic-dependabot-update-graph",
		RunID: 30152132020, RunAttempt: 1, JobID: 89664191125,
		RawCreatedAt:   time.Date(2026, 7, 25, 9, 1, 57, 0, time.UTC),
		RawStartedAt:   time.Date(2026, 7, 25, 9, 3, 45, 0, time.UTC),
		RawCompletedAt: time.Date(2026, 7, 25, 9, 4, 9, 0, time.UTC),
		RawConclusion:  "success", TimingEndpointRunnerClass: "UBUNTU", TimingEndpointTotalMillis: &zero,
		DerivedRunnerMinutes: &one, RunnerMinutesSource: "retained-external-job-execution-duration",
		RunnerMinutesDerivation: "ceil((2026-07-25T09:04:09Z - 2026-07-25T09:03:45Z)/60000)=1",
		MatchedRunnerMinutes:    true, DualRunEligible: false, BlocksP0: false,
	}
}

func timestampAnomalyGapRow() AcceptanceGapRow {
	zero := int64(0)

	return AcceptanceGapRow{
		ID: "gap-provider-timestamp-anomaly-30151461867-89662641145", Classification: "provider-timestamp-anomaly",
		Owner: "graith-maintainers", ApprovalSource: "graith message msg_2cc3c01a495e63d3", ExpiresOn: "2026-08-31",
		Rationale:  "GitHub returned completed_at before started_at for a cancelled repo-owned job; raw values are retained without normalization",
		WorkflowID: "goreleaser", WorkflowPath: ".github/workflows/goreleaser.yml", Event: "pull_request",
		Ref:        "d0ugal/graith/issue-1637-upgrade-recovery-b0466102",
		EventShape: "push/release-candidate", ChangeID: "697c4c5d53721e3eccbd33008b77c51e6c4bc809",
		ModeCoordinate: "goreleaser/release-context", LatencyPolicyID: "latency-release-candidate",
		RunID: 30151461867, RunAttempt: 1, JobID: 89662641145,
		RawCreatedAt:   time.Date(2026, 7, 25, 8, 41, 15, 0, time.UTC),
		RawStartedAt:   time.Date(2026, 7, 25, 8, 41, 15, 0, time.UTC),
		RawCompletedAt: time.Date(2026, 7, 25, 8, 41, 14, 0, time.UTC),
		RawConclusion:  "cancelled", TimingEndpointRunnerClass: "UBUNTU", TimingEndpointTotalMillis: &zero,
		MatchedRunnerMinutes: false, DualRunEligible: false, BlocksP0: true,
	}
}

func finalizeAcceptanceForTest(t *testing.T, manifest AcceptanceManifest) AcceptanceManifest {
	t.Helper()

	finalized, err := FinalizeAcceptanceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}

	return finalized
}

func markAcceptanceSatisfiedForTest(t *testing.T, manifest *AcceptanceManifest) {
	t.Helper()

	manifest.SignOff = AcceptanceSignOff{
		OwnerReviewed:  true,
		Owner:          "ci-north-star-rollout",
		ReviewedAt:     time.Date(2026, 7, 26, 9, 30, 24, 0, time.UTC),
		ApprovalSource: p0SignOffApprovalSource,
		Rationale:      "synthetic test sign-off",
	}

	manifest.Result = AcceptanceResult{P0ExitSatisfied: true}
	rebindSignOffForTest(t, manifest)
}

func rebindSignOffForTest(t *testing.T, manifest *AcceptanceManifest) {
	t.Helper()

	reviewedDigest, err := preSignOffManifestDigest(*manifest)
	if err != nil {
		t.Fatal(err)
	}

	manifest.SignOff.ReviewedManifestDigest = reviewedDigest
	manifest.SignOff.InventoryDigest = manifest.InventoryDigest
	manifest.SignOff.ModeMatrixDigest = manifest.EvidencePackage.ModeMatrixDigest
	manifest.SignOff.WindowBundleDigest = manifest.EvidencePackage.WindowBundleDigest
	manifest.SignOff.RepoEvidenceDigest = manifest.EvidencePackage.RepoEvidenceDigest
	manifest.SignOff.ExternalRunDigests = append([]string(nil), manifest.EvidencePackage.ExternalRunDigests...)
	manifest.SignOff.ObservedCellCount = manifest.EvidencePackage.ObservedCellCount
	manifest.SignOff.GapRowCount = manifest.EvidencePackage.GapRowCount

	if manifest.InventoryRebind == nil {
		manifest.SignOff.InventoryRebindFromDigest = ""
		manifest.SignOff.InventoryRebindToDigest = ""

		return
	}

	manifest.SignOff.InventoryRebindFromDigest = manifest.InventoryRebind.FromDigest
	manifest.SignOff.InventoryRebindToDigest = manifest.InventoryRebind.ToDigest
}

func int64Ptr(value int64) *int64 {
	return &value
}
