package cipolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHermeticFixtureAcceptsCompleteCleanRun(t *testing.T) {
	run, _ := baseFixtureRun(t)

	report, err := RunHermeticFixture(run)
	if err != nil {
		t.Fatal(err)
	}

	if report.Status != "passed" {
		t.Fatalf("status = %s, want passed", report.Status)
	}
}

func TestHermeticFixtureRejectsChangeDetectionFaults(t *testing.T) {
	tests := map[string]struct {
		edit func(*FixtureRun)
		want string
	}{
		"unknown file list": {
			edit: func(run *FixtureRun) {
				run.PlanOptions.ExactFileList = false
			},
			want: "exact changed-file list",
		},
		"missing file": {
			edit: func(run *FixtureRun) {
				run.PlanOptions.ChangedFiles = []string{"internal/daemon/missing.go"}
			},
			want: "missing from the hermetic fixture",
		},
		"unknown detector path": {
			edit: func(run *FixtureRun) {
				run.KnownFiles = append(run.KnownFiles, FixtureFile{
					Path:   "bothy/blether.txt",
					SHA256: strings.Repeat("4", 64),
				})
				run.PlanOptions.ChangedFiles = []string{"bothy/blether.txt"}
			},
			want: "unknown to the hermetic fixture detector",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			run, _ := baseFixtureRun(t)

			faulted, err := ApplyFault(run, FaultInjection{ID: name, Apply: test.edit})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := RunHermeticFixture(faulted); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RunHermeticFixture() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestApplyFaultClonesNestedFixtureInputs(t *testing.T) {
	run, plan := baseFixtureRun(t)
	run.CacheRestores = []CacheRestoreCheck{validCacheRestoreCheck(plan)}

	originalChangedFile := run.PlanOptions.ChangedFiles[0]
	originalModeRef := run.Manifest.Modes[0].EvidenceRefs[0]
	originalKnownFile := append([]byte(nil), run.KnownFiles[0].Content...)
	originalCacheRequestPlanTrust := run.CacheRestores[0].Request.Plan.TrustTier
	originalCacheRequestJobMatrix := cloneStringMap(run.CacheRestores[0].Request.Job.Matrix)

	mutated, err := ApplyFault(run, FaultInjection{
		ID: "nested clone",
		Apply: func(run *FixtureRun) {
			run.PlanOptions.ChangedFiles[0] = "internal/cipolicy/fixture.go"
			run.Manifest.Modes[0].EvidenceRefs[0] = "dreich"
			run.KnownFiles[0].Content[0] = '^'
			run.CacheRestores[0].Request.Plan.TrustTier = "fork-untrusted"
			run.CacheRestores[0].Request.Job.Matrix["runner"] = "dreich"
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if mutated.PlanOptions.ChangedFiles[0] == originalChangedFile {
		t.Fatal("fault did not mutate cloned changed files")
	}

	if mutated.CacheRestores[0].Request.Plan.TrustTier == originalCacheRequestPlanTrust {
		t.Fatal("fault did not mutate cloned cache request plan")
	}

	if run.PlanOptions.ChangedFiles[0] != originalChangedFile {
		t.Fatalf("original changed file = %s, want %s", run.PlanOptions.ChangedFiles[0], originalChangedFile)
	}

	if run.Manifest.Modes[0].EvidenceRefs[0] != originalModeRef {
		t.Fatalf("original evidence ref = %s, want %s", run.Manifest.Modes[0].EvidenceRefs[0], originalModeRef)
	}

	if !slices.Equal(run.KnownFiles[0].Content, originalKnownFile) {
		t.Fatal("original known file content was mutated")
	}

	if run.CacheRestores[0].Request.Plan.TrustTier != originalCacheRequestPlanTrust {
		t.Fatalf("original cache request plan trust = %s, want %s", run.CacheRestores[0].Request.Plan.TrustTier, originalCacheRequestPlanTrust)
	}

	if !reflect.DeepEqual(run.CacheRestores[0].Request.Job.Matrix, originalCacheRequestJobMatrix) {
		t.Fatalf("original cache request job matrix = %#v, want %#v", run.CacheRestores[0].Request.Job.Matrix, originalCacheRequestJobMatrix)
	}
}

func TestHermeticEnvironmentRejectsPollution(t *testing.T) {
	tests := map[string]struct {
		edit func(map[string]string)
		want string
	}{
		"PATH": {
			edit: func(env map[string]string) {
				env["PATH"] = "/tmp/croft/bin:/usr/bin:/bin"
			},
			want: "polluted PATH",
		},
		"locale": {
			edit: func(env map[string]string) {
				env["LC_ALL"] = "en_US.UTF-8"
			},
			want: "polluted locale",
		},
		"timezone": {
			edit: func(env map[string]string) {
				env["TZ"] = "Europe/London"
			},
			want: "polluted timezone",
		},
		"compiler": {
			edit: func(env map[string]string) {
				env["CGO_CFLAGS"] = "-I/tmp/croft"
			},
			want: "compiler environment variable",
		},
		"credential": {
			edit: func(env map[string]string) {
				env["GH_TOKEN"] = "synthetic-secret"
			},
			want: "credential environment variable",
		},
		"empty credential": {
			edit: func(env map[string]string) {
				env["OPENAI_API_KEY"] = ""
			},
			want: "credential environment variable",
		},
		"cloud credential": {
			edit: func(env map[string]string) {
				env["AWS_ACCESS_KEY_ID"] = "synthetic-secret"
			},
			want: "credential environment variable",
		},
		"proxy": {
			edit: func(env map[string]string) {
				env["HTTPS_PROXY"] = "http://croft.invalid:8080"
			},
			want: "network environment variable",
		},
		"go proxy": {
			edit: func(env map[string]string) {
				env["GOPROXY"] = "https://proxy.golang.org"
			},
			want: "network environment variable",
		},
		"go target": {
			edit: func(env map[string]string) {
				env["GOOS"] = "darwin"
			},
			want: "compiler environment variable",
		},
		"toolchain flag": {
			edit: func(env map[string]string) {
				env["GOFLAGS"] = "-mod=mod"
			},
			want: "compiler environment variable",
		},
		"unexpected empty variable": {
			edit: func(env map[string]string) {
				env["HOME"] = ""
			},
			want: "unexpected environment variable",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			env := canonicalFixtureEnv()
			test.edit(env)

			if err := ValidateHermeticEnvironment(env); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateHermeticEnvironment() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGeneratedWorkflowDataIsBoundToManifestAndPlan(t *testing.T) {
	_, plan := baseFixtureRun(t)
	data := GenerateWorkflowData(plan)

	if err := data.ValidateAgainstPlan(plan); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		edit func(*GeneratedWorkflowData)
		want string
	}{
		"stale manifest digest": {
			edit: func(data *GeneratedWorkflowData) {
				data.PolicyDigest = strings.Repeat("9", 64)
			},
			want: "not bound",
		},
		"missing coordinate": {
			edit: func(data *GeneratedWorkflowData) {
				data.Jobs = data.Jobs[:len(data.Jobs)-1]
			},
			want: "jobs do not match",
		},
		"duplicate coordinate": {
			edit: func(data *GeneratedWorkflowData) {
				data.Jobs = append(data.Jobs, data.Jobs[0])
			},
			want: "not canonical",
		},
		"misleading generated name": {
			edit: func(data *GeneratedWorkflowData) {
				data.Jobs[0].GitHubName = "Canny green gate"
			},
			want: "jobs do not match",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := cloneGeneratedWorkflowData(data)
			test.edit(&mutated)

			if err := mutated.ValidateAgainstPlan(plan); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateAgainstPlan() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHermeticFixtureRequiresGeneratedWorkflowData(t *testing.T) {
	run, _ := baseFixtureRun(t)
	run.WorkflowData = GeneratedWorkflowData{}

	if _, err := RunHermeticFixture(run); err == nil || !strings.Contains(err.Error(), "requires generated workflow data") {
		t.Fatalf("RunHermeticFixture() error = %v, want missing generated data rejection", err)
	}
}

func TestHermeticFixtureBindsManifestWorkflowFiles(t *testing.T) {
	run, _ := baseFixtureRun(t)

	workflowPaths := make([]string, 0)
	for path := range workflowPathsForManifest(t, run.Manifest) {
		workflowPaths = append(workflowPaths, path)
	}

	slices.Sort(workflowPaths)

	if len(workflowPaths) == 0 {
		t.Fatal("manifest has no workflow traces")
	}

	workflowPath := workflowPaths[0]

	tests := map[string]struct {
		edit func(*FixtureRun)
		want string
	}{
		"missing workflow file": {
			edit: func(run *FixtureRun) {
				run.KnownFiles = removeFixtureFile(run.KnownFiles, workflowPath)
			},
			want: "missing from the hermetic fixture",
		},
		"missing workflow content": {
			edit: func(run *FixtureRun) {
				file := fixtureFileByPath(t, run.KnownFiles, workflowPath)
				run.KnownFiles[file].Content = nil
			},
			want: "requires hermetic file content",
		},
		"known file content mismatch": {
			edit: func(run *FixtureRun) {
				file := fixtureFileByPath(t, run.KnownFiles, workflowPath)
				run.KnownFiles[file].Content = append(run.KnownFiles[file].Content, '\n')
			},
			want: "content digest does not match",
		},
		"manifest workflow digest mismatch": {
			edit: func(run *FixtureRun) {
				replaceWorkflowDigest(t, &run.Manifest, workflowPath, strings.Repeat("9", 64))
			},
			want: "digest mismatch",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := cloneFixtureRun(run)
			test.edit(&mutated)

			if _, err := RunHermeticFixture(mutated); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RunHermeticFixture() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestFanInFixtureRejectsDeterministicResultFaults(t *testing.T) {
	tests := map[string]struct {
		edit func(*FixtureRun)
		want string
	}{
		"cancelled": {
			edit: func(run *FixtureRun) {
				run.Observations[0].Status = "cancelled"
				run.Observations[0].FailureClass = "policy"
			},
			want: "result-status-not-green",
		},
		"timed out": {
			edit: func(run *FixtureRun) {
				run.Observations[0].Status = "timed-out"
			},
			want: "result-status-not-green",
		},
		"superseded": {
			edit: func(run *FixtureRun) {
				run.Observations[0].Status = "superseded"
				run.Observations[0].FailureClass = "policy"
				run.Observations[0].SupersededBy = strings.Repeat("5", 64)
			},
			want: "result-status-not-green",
		},
		"success cannot hide supersession identity": {
			edit: func(run *FixtureRun) {
				run.Observations[0].Status = "success"
				run.Observations[0].SupersededBy = strings.Repeat("5", 64)
			},
			want: "successful result cannot be superseded",
		},
		"success cannot omit completion timestamp": {
			edit: func(run *FixtureRun) {
				run.Observations[0].Status = "success"
				run.Observations[0].UploadComplete = true
				run.Observations[0].CompletedAt = time.Time{}
			},
			want: "invalid timestamps",
		},
		"partial upload": {
			edit: func(run *FixtureRun) {
				run.Observations[0].UploadComplete = false
			},
			want: "partial upload",
		},
		"missing coordinate": {
			edit: func(run *FixtureRun) {
				run.Observations = run.Observations[:len(run.Observations)-1]
			},
			want: "missing-result",
		},
		"duplicate coordinate": {
			edit: func(run *FixtureRun) {
				run.Observations = append(run.Observations, run.Observations[0])
			},
			want: "duplicate-result",
		},
		"unknown coordinate": {
			edit: func(run *FixtureRun) {
				run.Observations[0].Coordinate = "legacy/dreich/blether"
			},
			want: "unknown coordinate",
		},
		"misleading display name": {
			edit: func(run *FixtureRun) {
				run.Observations[0].Display = "Green gate"
			},
			want: "misleading display name",
		},
		"requested mode did not run": {
			edit: func(run *FixtureRun) {
				missingMode := run.Observations[0].Mode

				filtered := run.Observations[:0]
				for _, observation := range run.Observations {
					if observation.Mode != missingMode {
						filtered = append(filtered, observation)
					}
				}

				run.Observations = filtered
			},
			want: "missing-result",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			run, _ := baseFixtureRun(t)
			test.edit(&run)

			report, err := RunHermeticFixture(run)
			if err == nil {
				t.Fatalf("RunHermeticFixture() succeeded with report %#v", report)
			}

			if !strings.Contains(err.Error(), test.want) && !decisionReasonsContain(report.Rejected, test.want) {
				t.Fatalf("error = %v rejected = %#v, want %q", err, report.Rejected, test.want)
			}
		})
	}
}

func TestCacheRestoreRejectsStaleCorruptAndCrossTrustEntries(t *testing.T) {
	_, plan := baseFixtureRun(t)
	job := plan.Jobs[0]
	writtenBy := plan.TrustTier + " build"
	writerDigest := cacheWriterDigest(plan, job, writtenBy)

	request := CacheRequest{
		Plan:            plan,
		Job:             job,
		Key:             "go-mod-linux-braw",
		ToolchainDigest: strings.Repeat("1", 64),
		Checksum:        strings.Repeat("2", 64),
		TrustTier:       plan.TrustTier,
		Now:             p2TestNow,
	}
	entry := CacheEntry{
		Key:             request.Key,
		ToolchainDigest: request.ToolchainDigest,
		Checksum:        request.Checksum,
		TrustTier:       request.TrustTier,
		WrittenBy:       writtenBy,
		WriterDigest:    writerDigest,
		PlanDigest:      plan.PlanDigest,
		PolicyDigest:    plan.PolicyDigest,
		SourceCommit:    plan.Source.Commit,
		SourceTree:      plan.Source.Tree,
		Mode:            job.Mode,
		Coordinate:      job.Coordinate,
		CreatedAt:       p2TestNow.Add(-time.Hour),
		ExpiresAt:       p2TestNow.Add(time.Hour),
	}

	if err := VerifyCacheRestore(request, entry); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		edit func(*CacheEntry)
		want string
	}{
		"mismatched key": {
			edit: func(entry *CacheEntry) {
				entry.Key = "go-mod-linux-dreich"
			},
			want: "key mismatch",
		},
		"mismatched toolchain": {
			edit: func(entry *CacheEntry) {
				entry.ToolchainDigest = strings.Repeat("3", 64)
			},
			want: "toolchain digest mismatch",
		},
		"corrupt checksum": {
			edit: func(entry *CacheEntry) {
				entry.Checksum = strings.Repeat("4", 64)
			},
			want: "checksum mismatch",
		},
		"untrusted writer": {
			edit: func(entry *CacheEntry) {
				entry.TrustTier = "fork-untrusted"
				entry.WrittenBy = "fork-untrusted branch"
			},
			want: "cannot satisfy",
		},
		"self-declared trusted writer without matching proof": {
			edit: func(entry *CacheEntry) {
				entry.TrustTier = request.TrustTier
				entry.WrittenBy = writtenBy
				entry.WriterDigest = strings.Repeat("f", 64)
			},
			want: "writer provenance",
		},
		"contradictory writer label": {
			edit: func(entry *CacheEntry) {
				entry.WrittenBy = "fork-untrusted pull_request build"
			},
			want: "writer provenance",
		},
		"cross commit cache": {
			edit: func(entry *CacheEntry) {
				entry.SourceCommit = strings.Repeat("6", 40)
			},
			want: "writer provenance",
		},
		"wrong coordinate cache": {
			edit: func(entry *CacheEntry) {
				entry.Coordinate = "legacy/ci/dreich[runner=ubuntu-latest]"
			},
			want: "writer provenance",
		},
		"stale cache": {
			edit: func(entry *CacheEntry) {
				entry.ExpiresAt = p2TestNow.Add(-time.Minute)
			},
			want: "expired",
		},
		"missing creation time": {
			edit: func(entry *CacheEntry) {
				entry.CreatedAt = time.Time{}
			},
			want: "creation and expiry times",
		},
		"missing expiry time": {
			edit: func(entry *CacheEntry) {
				entry.ExpiresAt = time.Time{}
			},
			want: "creation and expiry times",
		},
		"future creation time": {
			edit: func(entry *CacheEntry) {
				entry.CreatedAt = p2TestNow.Add(time.Minute)
			},
			want: "created in the future",
		},
		"creation after expiry": {
			edit: func(entry *CacheEntry) {
				entry.CreatedAt = p2TestNow.Add(-time.Minute)
				entry.ExpiresAt = p2TestNow.Add(-time.Hour)
			},
			want: "creation time must be before expiry",
		},
		"missing writer": {
			edit: func(entry *CacheEntry) {
				entry.WrittenBy = ""
			},
			want: "writer identity",
		},
		"missing writer proof": {
			edit: func(entry *CacheEntry) {
				entry.WriterDigest = ""
			},
			want: "writer proof digest",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := entry
			test.edit(&mutated)

			if err := VerifyCacheRestore(request, mutated); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyCacheRestore() error = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("missing request trust tier", func(t *testing.T) {
		mutated := request
		mutated.TrustTier = ""

		if err := VerifyCacheRestore(mutated, entry); err == nil || !strings.Contains(err.Error(), "trust tier is required") {
			t.Fatalf("VerifyCacheRestore() error = %v, want trust tier rejection", err)
		}
	})

	t.Run("missing entry trust tier", func(t *testing.T) {
		mutated := entry
		mutated.TrustTier = ""

		if err := VerifyCacheRestore(request, mutated); err == nil || !strings.Contains(err.Error(), "trust tier is required") {
			t.Fatalf("VerifyCacheRestore() error = %v, want trust tier rejection", err)
		}
	})

	t.Run("missing request plan identity", func(t *testing.T) {
		mutated := request
		mutated.Plan = RunPlan{}

		if err := VerifyCacheRestore(mutated, entry); err == nil || !strings.Contains(err.Error(), "plan identity") {
			t.Fatalf("VerifyCacheRestore() error = %v, want plan identity rejection", err)
		}
	})

	t.Run("partial request plan identity cannot mint writer proof", func(t *testing.T) {
		mutated := request
		mutated.Plan = RunPlan{TrustTier: plan.TrustTier}

		mutatedEntry := entry
		mutatedEntry.PlanDigest = ""
		mutatedEntry.PolicyDigest = ""
		mutatedEntry.SourceCommit = ""
		mutatedEntry.SourceTree = ""
		mutatedEntry.WriterDigest = cacheWriterDigest(mutated.Plan, mutated.Job, mutatedEntry.WrittenBy)

		if err := VerifyCacheRestore(mutated, mutatedEntry); err == nil || !strings.Contains(err.Error(), "complete plan identity") {
			t.Fatalf("VerifyCacheRestore() error = %v, want complete plan identity rejection", err)
		}
	})

	t.Run("missing request job identity", func(t *testing.T) {
		mutated := request
		mutated.Job = PlanJob{}

		if err := VerifyCacheRestore(mutated, entry); err == nil || !strings.Contains(err.Error(), "job identity") {
			t.Fatalf("VerifyCacheRestore() error = %v, want job identity rejection", err)
		}
	})

	t.Run("partial request job identity cannot mint writer proof", func(t *testing.T) {
		mutated := request
		mutated.Job = PlanJob{
			TrustTier:  plan.TrustTier,
			Mode:       job.Mode,
			Coordinate: job.Coordinate,
		}

		mutatedEntry := entry
		mutatedEntry.WriterDigest = cacheWriterDigest(mutated.Plan, mutated.Job, mutatedEntry.WrittenBy)

		if err := VerifyCacheRestore(mutated, mutatedEntry); err == nil || !strings.Contains(err.Error(), "complete job identity") {
			t.Fatalf("VerifyCacheRestore() error = %v, want complete job identity rejection", err)
		}
	})
}

func TestHermeticFixtureRunsDeterministicSideEffectValidators(t *testing.T) {
	run, plan := baseFixtureRun(t)
	job := plan.Jobs[0]
	writtenBy := plan.TrustTier + " build"
	run.CacheRestores = []CacheRestoreCheck{{
		Job: job,
		Request: CacheRequest{
			Key:             "go-mod-linux-braw",
			ToolchainDigest: strings.Repeat("1", 64),
			Checksum:        strings.Repeat("2", 64),
			TrustTier:       plan.TrustTier,
			Now:             p2TestNow,
		},
		Entry: CacheEntry{
			Key:             "go-mod-linux-dreich",
			ToolchainDigest: strings.Repeat("1", 64),
			Checksum:        strings.Repeat("2", 64),
			TrustTier:       plan.TrustTier,
			WrittenBy:       writtenBy,
			WriterDigest:    cacheWriterDigest(plan, job, writtenBy),
			PlanDigest:      plan.PlanDigest,
			PolicyDigest:    plan.PolicyDigest,
			SourceCommit:    plan.Source.Commit,
			SourceTree:      plan.Source.Tree,
			Mode:            job.Mode,
			Coordinate:      job.Coordinate,
			CreatedAt:       p2TestNow.Add(-time.Hour),
			ExpiresAt:       p2TestNow.Add(time.Hour),
		},
	}}

	if _, err := RunHermeticFixture(run); err == nil || !strings.Contains(err.Error(), "key mismatch") {
		t.Fatalf("RunHermeticFixture() error = %v, want cache key mismatch", err)
	}
}

func TestHermeticFixtureBindsSideEffectsToEvaluatedPlan(t *testing.T) {
	tests := map[string]struct {
		edit func(*FixtureRun, RunPlan)
		want string
	}{
		"cache restore cannot relabel both sides as another trust tier": {
			edit: func(run *FixtureRun, plan RunPlan) {
				job := plan.Jobs[0]
				writtenBy := "trusted-base build"
				run.CacheRestores = []CacheRestoreCheck{{
					Job: job,
					Request: CacheRequest{
						Key:             "go-mod-linux-braw",
						ToolchainDigest: strings.Repeat("1", 64),
						Checksum:        strings.Repeat("2", 64),
						TrustTier:       "trusted-base",
					},
					Entry: CacheEntry{
						Key:             "go-mod-linux-braw",
						ToolchainDigest: strings.Repeat("1", 64),
						Checksum:        strings.Repeat("2", 64),
						TrustTier:       "trusted-base",
						WrittenBy:       writtenBy,
						WriterDigest:    cacheWriterDigest(plan, job, writtenBy),
						PlanDigest:      plan.PlanDigest,
						PolicyDigest:    plan.PolicyDigest,
						SourceCommit:    plan.Source.Commit,
						SourceTree:      plan.Source.Tree,
						Mode:            job.Mode,
						Coordinate:      job.Coordinate,
						CreatedAt:       p2TestNow.Add(-time.Hour),
						ExpiresAt:       p2TestNow.Add(time.Hour),
					},
				}}
			},
			want: "cache restore trust tier",
		},
		"cache restore cannot relabel writer metadata without producer proof": {
			edit: func(run *FixtureRun, plan RunPlan) {
				check := validCacheRestoreCheck(plan)
				check.Entry.WrittenBy = plan.TrustTier + " build"
				check.Entry.TrustTier = plan.TrustTier
				check.Entry.WriterDigest = strings.Repeat("f", 64)
				run.CacheRestores = []CacheRestoreCheck{check}
			},
			want: "cache writer provenance",
		},
		"cache restore cannot carry stale producer plan provenance": {
			edit: func(run *FixtureRun, plan RunPlan) {
				check := validCacheRestoreCheck(plan)
				check.Entry.PlanDigest = strings.Repeat("9", 64)
				check.Entry.PolicyDigest = strings.Repeat("8", 64)
				check.Entry.SourceCommit = strings.Repeat("7", 40)
				check.Entry.SourceTree = strings.Repeat("6", 40)
				run.CacheRestores = []CacheRestoreCheck{check}
			},
			want: "cache writer provenance",
		},
		"cache restore cannot carry stale validation time": {
			edit: func(run *FixtureRun, plan RunPlan) {
				writtenBy := plan.TrustTier + " build"
				run.CacheRestores = []CacheRestoreCheck{{
					Request: CacheRequest{
						Key:             "go-mod-linux-braw",
						ToolchainDigest: strings.Repeat("1", 64),
						Checksum:        strings.Repeat("2", 64),
						TrustTier:       plan.TrustTier,
						Now:             p2TestNow.Add(-2 * time.Hour),
					},
					Entry: CacheEntry{
						Key:             "go-mod-linux-braw",
						ToolchainDigest: strings.Repeat("1", 64),
						Checksum:        strings.Repeat("2", 64),
						TrustTier:       plan.TrustTier,
						WrittenBy:       writtenBy,
						WriterDigest:    cacheWriterDigest(plan, plan.Jobs[0], writtenBy),
						PlanDigest:      plan.PlanDigest,
						PolicyDigest:    plan.PolicyDigest,
						SourceCommit:    plan.Source.Commit,
						SourceTree:      plan.Source.Tree,
						Mode:            plan.Jobs[0].Mode,
						Coordinate:      plan.Jobs[0].Coordinate,
						CreatedAt:       p2TestNow.Add(-time.Hour),
						ExpiresAt:       p2TestNow.Add(time.Hour),
					},
				}}
			},
			want: "cache restore validation time",
		},
		"cache restore digest must match accepted result row": {
			edit: func(run *FixtureRun, plan RunPlan) {
				check := validCacheRestoreCheck(plan)
				run.CacheRestores = []CacheRestoreCheck{check}
				run.Observations[0].CacheDigest = strings.Repeat("d", 64)
			},
			want: "cache restore digest",
		},
		"artifact expectation cannot carry stale plan provenance": {
			edit: func(run *FixtureRun, plan RunPlan) {
				check := validArtifactCheck(plan)
				stalePlan := plan
				stalePlan.PlanDigest = strings.Repeat("9", 64)
				stalePlan.PolicyDigest = strings.Repeat("8", 64)
				stalePlan.Source.Commit = strings.Repeat("7", 40)
				stalePlan.Source.Tree = strings.Repeat("6", 40)

				check.Expectation.Plan = stalePlan
				check.Artifact.PlanDigest = stalePlan.PlanDigest
				check.Artifact.PolicyDigest = stalePlan.PolicyDigest
				check.Artifact.SourceCommit = stalePlan.Source.Commit
				check.Artifact.SourceTree = stalePlan.Source.Tree
				run.Artifacts = []ArtifactCheck{check}
			},
			want: "artifact expectation plan",
		},
		"artifact expectation job must come from the evaluated plan": {
			edit: func(run *FixtureRun, plan RunPlan) {
				check := validArtifactCheck(plan)
				check.Expectation.Job.Coordinate = "legacy/ci/dreich[runner=ubuntu-latest]"
				check.Artifact.Coordinate = check.Expectation.Job.Coordinate
				run.Artifacts = []ArtifactCheck{check}
			},
			want: "missing from the evaluated plan",
		},
		"artifact expectation cannot carry stale validation time": {
			edit: func(run *FixtureRun, plan RunPlan) {
				check := validArtifactCheck(plan)
				check.Expectation.Now = p2TestNow.Add(-time.Hour)
				run.Artifacts = []ArtifactCheck{check}
			},
			want: "artifact expectation validation time",
		},
		"artifact expectation cannot widen fixture max age": {
			edit: func(run *FixtureRun, plan RunPlan) {
				check := validArtifactCheck(plan)
				check.Expectation.MaxAge = 48 * time.Hour
				run.Artifacts = []ArtifactCheck{check}
			},
			want: "artifact expectation max age",
		},
		"artifact digest must match accepted result row": {
			edit: func(run *FixtureRun, plan RunPlan) {
				check := validArtifactCheck(plan)
				run.Artifacts = []ArtifactCheck{check}
				run.Observations[0].ArtifactDigest = strings.Repeat("d", 64)
			},
			want: "artifact digest",
		},
		"archive comparison must be backed by evaluated plan platforms": {
			edit: func(run *FixtureRun, _ RunPlan) {
				run.ArchiveComparisons = []ArchiveComparison{validArchiveComparison("haggis", "neeps")}
			},
			want: "archive comparison platform",
		},
		"pre-plan observation cannot satisfy current fixture run": {
			edit: func(run *FixtureRun, plan RunPlan) {
				run.Observations[0].StartedAt = plan.CreatedAt.Add(-time.Second)
				run.Observations[0].CompletedAt = plan.CreatedAt
			},
			want: "starts before evaluated plan",
		},
		"future observation cannot satisfy current fixture run": {
			edit: func(run *FixtureRun, _ RunPlan) {
				run.Observations[0].StartedAt = run.Now.Add(time.Minute)
				run.Observations[0].CompletedAt = run.Now.Add(2 * time.Minute)
			},
			want: "starts in the future",
		},
		"future observation completion cannot satisfy current fixture run": {
			edit: func(run *FixtureRun, _ RunPlan) {
				run.Observations[0].StartedAt = run.Now.Add(-time.Minute)
				run.Observations[0].CompletedAt = run.Now.Add(time.Minute)
			},
			want: "completes in the future",
		},
		"credential operation cannot declare trusted publication for same repository agent run": {
			edit: func(run *FixtureRun, _ RunPlan) {
				run.CredentialOperations = []CredentialOperation{{
					Operation: "regeneration-push",
					TrustTier: "trusted-publication",
					Token: SyntheticToken{
						Name:         "release",
						TrustTier:    "trusted-publication",
						Class:        syntheticMaintainerToken,
						Scopes:       []string{"contents:write"},
						AllowedRoots: []string{"generated"},
					},
					Target: "generated/braw.bundle",
				}}
			},
			want: "credential operation trust tier",
		},
		"credential operation must be backed by selected capability": {
			edit: func(run *FixtureRun, _ RunPlan) {
				run.CredentialOperations = []CredentialOperation{docsPreviewCredentialOperation()}
			},
			want: "requires plan capability docs-preview",
		},
		"credential operation cannot borrow another operation capability": {
			edit: func(run *FixtureRun, _ RunPlan) {
				docsRun, _ := docsPreviewFixtureRun(t)

				*run = docsRun
				run.CredentialOperations = []CredentialOperation{{
					Operation:  "coverage-comment",
					TrustTier:  "same-repository-agent",
					Capability: "docs-preview",
					Token: SyntheticToken{
						Name:         "repo-write",
						TrustTier:    "same-repository-agent",
						Class:        syntheticRepositoryWriteToken,
						Scopes:       []string{"pull-requests:write"},
						AllowedRoots: []string{"comments"},
					},
					Target: "comments/pr-17.md",
				}}
			},
			want: "cannot use plan capability docs-preview",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			run, plan := baseFixtureRun(t)
			test.edit(&run, plan)

			if _, err := RunHermeticFixture(run); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RunHermeticFixture() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHermeticFixtureBindsVerifiedSideEffectsToAcceptedRows(t *testing.T) {
	run, plan := baseFixtureRun(t)
	cache := validCacheRestoreCheck(plan)
	artifact := validArtifactCheck(plan)

	run.CacheRestores = []CacheRestoreCheck{cache}
	run.Artifacts = []ArtifactCheck{artifact}
	bindObservationSideEffectDigests(t, &run, cache, artifact)

	report, err := RunHermeticFixture(run)
	if err != nil {
		t.Fatalf("RunHermeticFixture() error = %v", err)
	}

	if report.Status != "passed" || len(report.Accepted) != len(plan.Jobs) {
		t.Fatalf("RunHermeticFixture() report = %+v, want all jobs accepted", report)
	}
}

func TestHermeticFixtureAcceptsPlanBoundCredentialOperation(t *testing.T) {
	run, _ := docsPreviewFixtureRun(t)
	run.CredentialOperations = []CredentialOperation{docsPreviewCredentialOperation()}

	if _, err := RunHermeticFixture(run); err != nil {
		t.Fatalf("RunHermeticFixture() error = %v", err)
	}
}

func TestArtifactVerificationRejectsCorruptSubstitutedStaleAndCrossCommitArtifacts(t *testing.T) {
	_, plan := baseFixtureRun(t)
	job := plan.Jobs[0]
	expect := ArtifactExpectation{
		Plan:          plan,
		Job:           job,
		Digest:        strings.Repeat("7", 64),
		ProducerRunID: "run-17",
		RunAttempt:    1,
		Now:           p2TestNow,
	}
	artifact := ArtifactManifest{
		Name:           "braw-artifact",
		Digest:         expect.Digest,
		ContentDigest:  expect.Digest,
		PlanDigest:     plan.PlanDigest,
		PolicyDigest:   plan.PolicyDigest,
		SourceCommit:   plan.Source.Commit,
		SourceTree:     plan.Source.Tree,
		Mode:           job.Mode,
		Coordinate:     job.Coordinate,
		ProducerRunID:  expect.ProducerRunID,
		RunAttempt:     expect.RunAttempt,
		CreatedAt:      p2TestNow.Add(-time.Hour),
		UploadComplete: true,
	}

	if err := VerifyArtifact(expect, artifact); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		edit func(*ArtifactManifest)
		want string
	}{
		"corrupt content digest": {
			edit: func(artifact *ArtifactManifest) {
				artifact.ContentDigest = strings.Repeat("8", 64)
			},
			want: "digest mismatch",
		},
		"substituted producer run": {
			edit: func(artifact *ArtifactManifest) {
				artifact.ProducerRunID = "run-18"
			},
			want: "producer run identity",
		},
		"stale artifact": {
			edit: func(artifact *ArtifactManifest) {
				artifact.CreatedAt = p2TestNow.Add(-25 * time.Hour)
			},
			want: "stale",
		},
		"cross commit artifact": {
			edit: func(artifact *ArtifactManifest) {
				artifact.SourceCommit = strings.Repeat("6", 40)
			},
			want: "provenance",
		},
		"partial upload": {
			edit: func(artifact *ArtifactManifest) {
				artifact.UploadComplete = false
			},
			want: "partially uploaded",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := artifact
			test.edit(&mutated)

			if err := VerifyArtifact(expect, mutated); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyArtifact() error = %v, want %q", err, test.want)
			}
		})
	}

	missingIdentityTests := map[string]struct {
		edit func(*ArtifactExpectation, *ArtifactManifest)
		want string
	}{
		"missing expected job coordinate": {
			edit: func(expect *ArtifactExpectation, _ *ArtifactManifest) {
				expect.Job = PlanJob{}
			},
			want: "job coordinate identity",
		},
		"missing artifact job coordinate": {
			edit: func(_ *ArtifactExpectation, artifact *ArtifactManifest) {
				artifact.Coordinate = ""
			},
			want: "job coordinate identity",
		},
		"missing expected producer": {
			edit: func(expect *ArtifactExpectation, _ *ArtifactManifest) {
				expect.ProducerRunID = ""
			},
			want: "producer run identity",
		},
		"missing artifact producer": {
			edit: func(_ *ArtifactExpectation, artifact *ArtifactManifest) {
				artifact.ProducerRunID = ""
			},
			want: "producer run identity",
		},
		"missing expected run attempt": {
			edit: func(expect *ArtifactExpectation, _ *ArtifactManifest) {
				expect.RunAttempt = 0
			},
			want: "run attempt must be positive",
		},
		"missing artifact run attempt": {
			edit: func(_ *ArtifactExpectation, artifact *ArtifactManifest) {
				artifact.RunAttempt = 0
			},
			want: "run attempt must be positive",
		},
	}

	for name, test := range missingIdentityTests {
		t.Run(name, func(t *testing.T) {
			mutatedExpect := expect
			mutatedArtifact := artifact
			test.edit(&mutatedExpect, &mutatedArtifact)

			if err := VerifyArtifact(mutatedExpect, mutatedArtifact); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyArtifact() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPortableArchiveComparisonRejectsPlatformDifferences(t *testing.T) {
	left := ArchiveSnapshot{
		Platform: "linux",
		Entries: []ArchiveEntry{
			{Name: "README.md", Type: "file", Mode: 0o644, SHA256: strings.Repeat("1", 64), LineEnding: "lf"},
			{Name: "bin/gr", Type: "file", Mode: 0o755, SHA256: strings.Repeat("2", 64), LineEnding: "binary"},
			{Name: "share/current", Type: "symlink", Mode: 0o777, SHA256: strings.Repeat("3", 64), LineEnding: "none", LinkTarget: "releases/braw"},
		},
	}
	right := ArchiveSnapshot{
		Platform: "macos",
		Entries:  append([]ArchiveEntry(nil), left.Entries...),
	}

	if err := ComparePortableArchives(left, right); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		edit func(*ArchiveSnapshot)
		want string
	}{
		"ordering": {
			edit: func(snapshot *ArchiveSnapshot) {
				slices.Reverse(snapshot.Entries)
			},
			want: "order differs",
		},
		"mode bit": {
			edit: func(snapshot *ArchiveSnapshot) {
				snapshot.Entries[1].Mode = 0o644
			},
			want: "mode differs",
		},
		"line ending": {
			edit: func(snapshot *ArchiveSnapshot) {
				snapshot.Entries[0].LineEnding = "crlf"
			},
			want: "line ending differs",
		},
		"symlink": {
			edit: func(snapshot *ArchiveSnapshot) {
				snapshot.Entries[2].LinkTarget = "releases/dreich"
			},
			want: "symlink target differs",
		},
		"same platform": {
			edit: func(snapshot *ArchiveSnapshot) {
				snapshot.Platform = left.Platform
			},
			want: "distinct platforms",
		},
		"invalid digest": {
			edit: func(snapshot *ArchiveSnapshot) {
				snapshot.Entries[0].SHA256 = "dreich"
			},
			want: "invalid digest",
		},
		"empty symlink": {
			edit: func(snapshot *ArchiveSnapshot) {
				snapshot.Entries[2].LinkTarget = ""
			},
			want: "empty symlink target",
		},
		"escaping symlink": {
			edit: func(snapshot *ArchiveSnapshot) {
				snapshot.Entries[2].LinkTarget = "../../etc/passwd"
			},
			want: "escapes the archive root",
		},
		"absolute symlink": {
			edit: func(snapshot *ArchiveSnapshot) {
				snapshot.Entries[2].LinkTarget = "/etc/passwd"
			},
			want: "escapes the archive root",
		},
		"duplicate member": {
			edit: func(snapshot *ArchiveSnapshot) {
				snapshot.Entries = append(snapshot.Entries, snapshot.Entries[1])
			},
			want: "duplicated",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := right
			mutated.Entries = append([]ArchiveEntry(nil), right.Entries...)
			test.edit(&mutated)

			if err := ComparePortableArchives(left, mutated); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ComparePortableArchives() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestUnsupportedPlatformCannotBeReportedAsPassed(t *testing.T) {
	manifest := manifestWithUnsupportedWindowsPlatform(t)

	plan, err := BuildHermeticPlan(manifest, fixtureKnownFiles(t, manifest), PlanOptions{
		Event:         planEvent(nil),
		ChangedFiles:  []string{"internal/daemon/daemon.go"},
		ExactFileList: true,
		CreatedAt:     p2TestNow,
		ExpiresAt:     p2TestNow.Add(time.Hour),
		Now:           p2TestNow,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Unsupported) == 0 {
		t.Fatal("plan has no unsupported decisions")
	}

	if err := RejectUnsupportedPlatformPasses(plan, nil); err != nil {
		t.Fatal(err)
	}

	unsupported := plan.Unsupported[0]

	data := GenerateWorkflowData(plan)
	if len(data.Unsupported) == 0 {
		t.Fatal("generated workflow data has no unsupported decisions")
	}

	data.Unsupported[0].Status = "passed"

	for _, status := range []string{"success", "passed", "skipped", "neutral"} {
		t.Run(status, func(t *testing.T) {
			err = RejectUnsupportedPlatformPasses(plan, []GeneratedUnsupported{{
				Mode:       unsupported.Mode,
				Coordinate: unsupported.Coordinate,
				Platform:   unsupported.Platform,
				TrustTier:  unsupported.TrustTier,
				Status:     status,
			}})
			if err == nil || !strings.Contains(err.Error(), "reported as passed") {
				t.Fatalf("RejectUnsupportedPlatformPasses() error = %v, want passed rejection", err)
			}
		})
	}

	_, err = FanInFixture(manifest, plan, data, successfulObservations(plan, p2TestNow), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "reported as passed") {
		t.Fatalf("FanInFixture() error = %v, want passed unsupported rejection", err)
	}
}

func TestSyntheticTokensDoNotUpgradeSameRepositoryAgentCredentials(t *testing.T) {
	tests := map[string]struct {
		operation CredentialOperation
		want      string
	}{
		"same repository docs preview uses repository write token": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "repo-write",
					TrustTier:    "same-repository-agent",
					Class:        syntheticRepositoryWriteToken,
					Scopes:       []string{"contents:write", "pull-requests:write"},
					AllowedRoots: []string{"screenshots"},
				},
				Target: "screenshots/pr-17/braw.png",
			},
		},
		"fork docs preview cannot use write token": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "fork-untrusted",
				Token: SyntheticToken{
					Name:         "repo-write",
					TrustTier:    "fork-untrusted",
					Class:        syntheticRepositoryWriteToken,
					Scopes:       []string{"contents:write", "pull-requests:write"},
					AllowedRoots: []string{"screenshots"},
				},
				Target: "screenshots/pr-17/braw.png",
			},
			want: "fork pull requests may use only synthetic read tokens",
		},
		"fork read token cannot use same repository publication operation": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "fork-untrusted",
				Token: SyntheticToken{
					Name:         "read",
					TrustTier:    "fork-untrusted",
					Class:        syntheticReadToken,
					Scopes:       []string{"contents:read"},
					AllowedRoots: []string{"screenshots"},
				},
				Target: "screenshots/pr-17/braw.png",
			},
			want: "not allowed for trust tier",
		},
		"same repository docs preview cannot use maintainer token": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "release",
					TrustTier:    "same-repository-agent",
					Class:        syntheticMaintainerToken,
					Scopes:       []string{"contents:write"},
					AllowedRoots: []string{"."},
				},
				Target: "screenshots/pr-17/braw.png",
			},
			want: "same-repository agent branches cannot obtain maintainer credentials",
		},
		"same repository docs preview requires closed operation spelling": {
			operation: CredentialOperation{
				Operation: "stable-release-publsh",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "repo-write",
					TrustTier:    "same-repository-agent",
					Class:        syntheticRepositoryWriteToken,
					Scopes:       []string{"contents:write"},
					AllowedRoots: []string{"generated"},
				},
				Target: "generated/braw.bundle",
			},
			want: "unsupported credential operation",
		},
		"same repository docs preview rejects overbroad token root": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "repo-write",
					TrustTier:    "same-repository-agent",
					Class:        syntheticRepositoryWriteToken,
					Scopes:       []string{"contents:write", "pull-requests:write"},
					AllowedRoots: []string{"."},
				},
				Target: "screenshots/pr-17/braw.png",
			},
			want: "outside the operation boundary",
		},
		"same repository docs preview rejects arbitrary repository path": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "repo-write",
					TrustTier:    "same-repository-agent",
					Class:        syntheticRepositoryWriteToken,
					Scopes:       []string{"contents:write", "pull-requests:write"},
					AllowedRoots: []string{"screenshots", "website"},
				},
				Target: "website/content/docs/braw.md",
			},
			want: "operation boundary",
		},
		"same repository docs preview requires expected scopes": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "repo-write",
					TrustTier:    "same-repository-agent",
					Class:        syntheticRepositoryWriteToken,
					Scopes:       []string{"contents:write"},
					AllowedRoots: []string{"screenshots"},
				},
				Target: "screenshots/pr-17/braw.png",
			},
			want: "missing required scope",
		},
		"same repository docs preview rejects extra scope": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "repo-write",
					TrustTier:    "same-repository-agent",
					Class:        syntheticRepositoryWriteToken,
					Scopes:       []string{"contents:write", "pull-requests:write", "actions:write"},
					AllowedRoots: []string{"screenshots"},
				},
				Target: "screenshots/pr-17/braw.png",
			},
			want: "unsupported scope",
		},
		"same repository docs preview rejects extra token root": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "repo-write",
					TrustTier:    "same-repository-agent",
					Class:        syntheticRepositoryWriteToken,
					Scopes:       []string{"contents:write", "pull-requests:write"},
					AllowedRoots: []string{"screenshots", "generated"},
				},
				Target: "screenshots/pr-17/braw.png",
			},
			want: "outside the operation boundary",
		},
		"same repository regeneration push cannot get maintainer token": {
			operation: CredentialOperation{
				Operation: "regeneration-push",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "release",
					TrustTier:    "same-repository-agent",
					Class:        syntheticMaintainerToken,
					Scopes:       []string{"contents:write"},
					AllowedRoots: []string{"generated"},
				},
				Target: "generated/braw.bundle",
			},
			want: "same-repository agent branches cannot obtain maintainer credentials",
		},
		"trusted regeneration push can use maintainer token inside its boundary": {
			operation: CredentialOperation{
				Operation: "regeneration-push",
				TrustTier: "trusted-publication",
				Token: SyntheticToken{
					Name:         "release",
					TrustTier:    "trusted-publication",
					Class:        syntheticMaintainerToken,
					Scopes:       []string{"contents:write"},
					AllowedRoots: []string{"generated"},
				},
				Target: "generated/braw.bundle",
			},
		},
		"coverage comment cannot use maintainer token": {
			operation: CredentialOperation{
				Operation: "coverage-comment",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "release",
					TrustTier:    "same-repository-agent",
					Class:        syntheticMaintainerToken,
					Scopes:       []string{"pull-requests:write"},
					AllowedRoots: []string{"comments"},
				},
				Target: "comments/pr-17.md",
			},
			want: "same-repository agent branches cannot obtain maintainer credentials",
		},
		"filesystem escape": {
			operation: CredentialOperation{
				Operation: "docs-preview-write",
				TrustTier: "same-repository-agent",
				Token: SyntheticToken{
					Name:         "repo-write",
					TrustTier:    "same-repository-agent",
					Class:        syntheticRepositoryWriteToken,
					Scopes:       []string{"contents:write", "pull-requests:write"},
					AllowedRoots: []string{"screenshots"},
				},
				Target: "website/../secrets/braw.txt",
			},
			want: "filesystem boundary",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateCredentialOperation(test.operation)
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateCredentialOperation() error = %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCredentialOperation() error = %v, want %q", err, test.want)
			}
		})
	}
}

func baseFixtureRun(t *testing.T) (FixtureRun, RunPlan) {
	t.Helper()

	options := PlanOptions{
		Event:         planEvent(nil),
		ChangedFiles:  []string{"internal/daemon/daemon.go"},
		ExactFileList: true,
		CreatedAt:     p2TestNow,
		ExpiresAt:     p2TestNow.Add(time.Hour),
		Now:           p2TestNow,
	}

	return fixtureRunForOptions(t, options, nil)
}

func docsPreviewFixtureRun(t *testing.T) (FixtureRun, RunPlan) {
	t.Helper()

	options := PlanOptions{
		Event:         planEvent(nil),
		ChangedFiles:  []string{"website/content/docs/contributing/ci-policy-fixture.md"},
		ExactFileList: true,
		CreatedAt:     p2TestNow,
		ExpiresAt:     p2TestNow.Add(time.Hour),
		Now:           p2TestNow,
	}

	return fixtureRunForOptions(t, options, []string{"website/content/docs/contributing/ci-policy-fixture.md"})
}

func fixtureRunForOptions(t *testing.T, options PlanOptions, extraKnownFiles []string) (FixtureRun, RunPlan) {
	t.Helper()

	manifest := loadManifest(t)
	knownFiles := fixtureKnownFiles(t, manifest)

	for _, path := range extraKnownFiles {
		knownFiles = append(knownFiles, fixtureFileFromRepo(t, path))
	}

	plan, err := BuildHermeticPlan(manifest, knownFiles, options)
	if err != nil {
		t.Fatal(err)
	}

	run := FixtureRun{
		Manifest:     manifest,
		KnownFiles:   knownFiles,
		Environment:  canonicalFixtureEnv(),
		PlanOptions:  options,
		WorkflowData: GenerateWorkflowData(plan),
		Observations: successfulObservations(plan, p2TestNow),
		Now:          p2TestNow,
	}

	return run, plan
}

func docsPreviewCredentialOperation() CredentialOperation {
	return CredentialOperation{
		Operation:  "docs-preview-write",
		TrustTier:  "same-repository-agent",
		Capability: "docs-preview",
		Token: SyntheticToken{
			Name:         "repo-write",
			TrustTier:    "same-repository-agent",
			Class:        syntheticRepositoryWriteToken,
			Scopes:       []string{"contents:write", "pull-requests:write"},
			AllowedRoots: []string{"screenshots"},
		},
		Target: "screenshots/pr-17/braw.png",
	}
}

func fixtureKnownFiles(t *testing.T, manifest Manifest) []FixtureFile {
	t.Helper()

	paths := []string{
		"internal/daemon/daemon.go",
		"internal/cipolicy/manifest.json",
		"go.sum",
	}

	for path := range workflowPathsForManifest(t, manifest) {
		paths = append(paths, path)
	}

	slices.Sort(paths)

	files := make([]FixtureFile, 0, len(paths))

	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}

		seen[path] = true
		files = append(files, fixtureFileFromRepo(t, path))
	}

	return files
}

func removeFixtureFile(files []FixtureFile, path string) []FixtureFile {
	filtered := files[:0]
	for _, file := range files {
		if normalizeChangedPath(file.Path) != path {
			filtered = append(filtered, file)
		}
	}

	return filtered
}

func fixtureFileByPath(t *testing.T, files []FixtureFile, path string) int {
	t.Helper()

	for index, file := range files {
		if normalizeChangedPath(file.Path) == path {
			return index
		}
	}

	t.Fatalf("fixture path %s not found", path)

	return -1
}

func replaceWorkflowDigest(t *testing.T, manifest *Manifest, path string, digest string) {
	t.Helper()

	replaced := false

	for modeIndex := range manifest.Modes {
		mode := &manifest.Modes[modeIndex]
		if normalizeChangedPath(mode.Trace.WorkflowPath) == path {
			mode.Trace.WorkflowSHA256 = digest
			replaced = true
		}

		for coordinateIndex := range mode.Coordinates {
			coordinate := &mode.Coordinates[coordinateIndex]
			if normalizeChangedPath(coordinate.Trace.WorkflowPath) == path {
				coordinate.Trace.WorkflowSHA256 = digest
				replaced = true
			}
		}
	}

	if !replaced {
		t.Fatalf("workflow path %s not found in manifest", path)
	}

	signManifest(t, manifest)
}

func workflowPathsForManifest(t *testing.T, manifest Manifest) map[string]bool {
	t.Helper()

	paths := map[string]bool{}
	for _, mode := range manifest.Modes {
		addWorkflowPath(t, paths, mode.Trace)

		for _, coordinate := range mode.Coordinates {
			addWorkflowPath(t, paths, coordinate.Trace)
		}
	}

	return paths
}

func addWorkflowPath(t *testing.T, paths map[string]bool, trace LegacyTrace) {
	t.Helper()

	path := normalizeChangedPath(trace.WorkflowPath)
	if path == "" {
		return
	}

	paths[path] = true
}

func fixtureFileFromRepo(t *testing.T, path string) FixtureFile {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}

	digest := sha256.Sum256(content)

	return FixtureFile{
		Path:    path,
		SHA256:  hex.EncodeToString(digest[:]),
		Content: content,
	}
}

func successfulObservations(plan RunPlan, started time.Time) []JobObservation {
	observations := make([]JobObservation, 0, len(plan.Jobs))

	windowStart := plan.CreatedAt
	if windowStart.IsZero() {
		windowStart = started
	}

	windowEnd := started
	if !plan.ExpiresAt.IsZero() && plan.ExpiresAt.Before(windowEnd) {
		windowEnd = plan.ExpiresAt
	}

	step := time.Duration(0)
	if windowEnd.After(windowStart) {
		step = windowEnd.Sub(windowStart) / time.Duration(len(plan.Jobs)+1)
	}

	for index, job := range plan.Jobs {
		startedAt := windowStart

		completedAt := windowStart
		if step > 0 {
			startedAt = windowStart.Add(time.Duration(index) * step)

			completedAt = startedAt.Add(step / 2)
			if completedAt.After(windowEnd) {
				completedAt = windowEnd
			}
		}

		observations = append(observations, JobObservation{
			Mode:           job.Mode,
			Coordinate:     job.Coordinate,
			Display:        job.GitHubName,
			Status:         "success",
			StartedAt:      startedAt,
			CompletedAt:    completedAt,
			EvidenceDigest: strings.Repeat("a", 64),
			ArtifactDigest: strings.Repeat("b", 64),
			CacheDigest:    strings.Repeat("c", 64),
			UploadComplete: true,
		})
	}

	return observations
}

func validCacheRestoreCheck(plan RunPlan) CacheRestoreCheck {
	job := plan.Jobs[0]
	checksum := strings.Repeat("c", 64)
	writtenBy := plan.TrustTier + " build"
	writerDigest := cacheWriterDigest(plan, job, writtenBy)

	return CacheRestoreCheck{
		Job: job,
		Request: CacheRequest{
			Plan:            plan,
			Job:             job,
			Key:             "go-mod-linux-braw",
			ToolchainDigest: strings.Repeat("1", 64),
			Checksum:        checksum,
			TrustTier:       plan.TrustTier,
			Now:             p2TestNow,
		},
		Entry: CacheEntry{
			Key:             "go-mod-linux-braw",
			ToolchainDigest: strings.Repeat("1", 64),
			Checksum:        checksum,
			TrustTier:       plan.TrustTier,
			WrittenBy:       writtenBy,
			WriterDigest:    writerDigest,
			PlanDigest:      plan.PlanDigest,
			PolicyDigest:    plan.PolicyDigest,
			SourceCommit:    plan.Source.Commit,
			SourceTree:      plan.Source.Tree,
			Mode:            job.Mode,
			Coordinate:      job.Coordinate,
			CreatedAt:       p2TestNow.Add(-time.Hour),
			ExpiresAt:       p2TestNow.Add(time.Hour),
		},
	}
}

func validArtifactCheck(plan RunPlan) ArtifactCheck {
	job := plan.Jobs[0]
	digest := strings.Repeat("7", 64)

	return ArtifactCheck{
		Expectation: ArtifactExpectation{
			Plan:          plan,
			Job:           job,
			Digest:        digest,
			ProducerRunID: "run-17",
			RunAttempt:    1,
			Now:           p2TestNow,
		},
		Artifact: ArtifactManifest{
			Name:           "braw-artifact",
			Digest:         digest,
			ContentDigest:  digest,
			PlanDigest:     plan.PlanDigest,
			PolicyDigest:   plan.PolicyDigest,
			SourceCommit:   plan.Source.Commit,
			SourceTree:     plan.Source.Tree,
			Mode:           job.Mode,
			Coordinate:     job.Coordinate,
			ProducerRunID:  "run-17",
			RunAttempt:     1,
			CreatedAt:      p2TestNow.Add(-time.Hour),
			UploadComplete: true,
		},
	}
}

func bindObservationSideEffectDigests(t *testing.T, run *FixtureRun, cache CacheRestoreCheck, artifact ArtifactCheck) {
	t.Helper()

	for index := range run.Observations {
		observation := &run.Observations[index]
		if observation.Mode != cache.Job.Mode || observation.Coordinate != cache.Job.Coordinate {
			continue
		}

		observation.CacheDigest = cache.Entry.Checksum
		observation.ArtifactDigest = artifact.Artifact.Digest

		return
	}

	t.Fatalf("no observation for side-effect coordinate %s/%s", cache.Job.Mode, cache.Job.Coordinate)
}

func validArchiveComparison(leftPlatform, rightPlatform string) ArchiveComparison {
	left := ArchiveSnapshot{
		Platform: leftPlatform,
		Entries: []ArchiveEntry{
			{Name: "README.md", Type: "file", Mode: 0o644, SHA256: strings.Repeat("1", 64), LineEnding: "lf"},
			{Name: "bin/gr", Type: "file", Mode: 0o755, SHA256: strings.Repeat("2", 64), LineEnding: "binary"},
			{Name: "share/current", Type: "symlink", Mode: 0o777, SHA256: strings.Repeat("3", 64), LineEnding: "none", LinkTarget: "releases/braw"},
		},
	}

	right := ArchiveSnapshot{
		Platform: rightPlatform,
		Entries:  append([]ArchiveEntry(nil), left.Entries...),
	}

	return ArchiveComparison{Left: left, Right: right}
}

func canonicalFixtureEnv() map[string]string {
	return map[string]string{
		"PATH":   FixtureCanonicalPath,
		"LC_ALL": FixtureCanonicalLocale,
		"LANG":   FixtureCanonicalLocale,
		"TZ":     FixtureCanonicalTimezone,
	}
}

func manifestWithUnsupportedWindowsPlatform(t *testing.T) Manifest {
	t.Helper()

	manifest := cloneManifest(t, loadManifest(t))
	manifest.Platforms = append(manifest.Platforms, Platform{
		ID:           "windows-latest",
		Owner:        "graith-maintainers",
		RunnerLabel:  "windows-latest",
		OS:           "windows",
		Architecture: "x64",
		Description:  "Unsupported fixture platform that must not pass silently.",
	})

	manifest.Unsupported = append(manifest.Unsupported, UnsupportedDecision{
		Coordinate:        "legacy/ci/windows[runner=windows-latest]",
		Source:            sourceID,
		Event:             "pull-request",
		Platform:          "windows-latest",
		TrustTier:         "same-repository-agent",
		Requiredness:      "unsupported",
		Owner:             "graith-maintainers",
		Rationale:         "Fixture-only unsupported Windows row for P3 fail-closed coverage.",
		Expires:           p2TestNow.AddDate(0, 1, 0).Format(time.DateOnly),
		SilentPassAllowed: false,
		EvidenceRefs: []string{
			"p0-inventory:" + manifest.Source.BaselineInventory.Digest + "#legacy/ci/windows[runner=windows-latest]",
		},
	})

	signManifest(t, &manifest)

	return manifest
}
