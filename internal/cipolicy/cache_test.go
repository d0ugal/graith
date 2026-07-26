package cipolicy

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheReadAcceptsMatchingTrustedIdentity(t *testing.T) {
	manifest := loadManifest(t)
	plan, job, result, cache, payload := cacheFixture(t, manifest, planEvent(func(event *EventInput) {
		event.SameRepositoryAgent = false
		event.TrustedBase = true
	}))

	if err := ValidateCacheRead(manifest, plan, result, plan, job, cache, payload, CacheReadOptions{
		Dependencies: artifactDependencies(),
		Toolchains:   artifactToolchains(),
		BuildFlags:   artifactBuildFlags(),
	}, p2TestNow); err != nil {
		t.Fatal(err)
	}
}

func TestCacheReadAcceptsExpiredProducerPlanForCurrentConsumer(t *testing.T) {
	manifest := loadManifest(t)
	files := cachePayloadFiles()
	payload := buildArtifactArchive(t, ArtifactFormatTar, cachePayloadMembers())
	cacheDigest := sha256Hex(payload)
	dependencies := artifactDependencies()
	toolchains := artifactToolchains()
	buildFlags := artifactBuildFlags()
	event := planEvent(nil)
	producerTime := p2TestNow.Add(-2 * time.Hour)
	producerPlan := buildTestPlanAt(t, manifest, event, producerTime, producerTime.Add(time.Hour), producerTime)
	producerJob := planJobByMode(t, producerPlan, "legacy/libghostty-native/native-gate")
	attempt := resultAttempt(1, "success", "", producerTime.Add(15*time.Minute))
	attempt.CacheDigest = cacheDigest

	result, err := NewResultRecord(producerPlan, producerJob, []ResultAttempt{attempt})
	if err != nil {
		t.Fatal(err)
	}

	cache, err := NewCacheManifest(manifest, producerPlan, result, CacheManifestInput{
		CacheFormat:  ArtifactFormatTar,
		CacheDigest:  cacheDigest,
		Dependencies: dependencies,
		Toolchains:   toolchains,
		BuildFlags:   buildFlags,
		Files:        files,
		Provenance: CacheProvenance{
			Workflow:       testProducerWorkflow,
			WorkflowSHA256: testProducerWorkflowSHA256,
			RunID:          testProducerRunID,
			RunAttempt:     1,
			JobID:          "native-gate",
			JobName:        "Native backend gate",
			ProducerStatus: "success",
			UploadComplete: true,
			CacheKey:       cacheKeyForResult(t, manifest, result, dependencies, toolchains, buildFlags),
			CacheDigest:    cacheDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	consumerPlan := buildTestPlanAt(t, manifest, event, p2TestNow, p2TestNow.Add(time.Hour), p2TestNow)
	consumerJob := planJobByMode(t, consumerPlan, "legacy/libghostty-native/native-gate")

	if err := ValidateCacheRead(manifest, producerPlan, result, consumerPlan, consumerJob, cache, payload, CacheReadOptions{
		Dependencies: dependencies,
		Toolchains:   toolchains,
		BuildFlags:   buildFlags,
	}, p2TestNow); err != nil {
		t.Fatal(err)
	}
}

func TestCacheReadWriteZeroNowUseCurrentTime(t *testing.T) {
	manifest := loadManifest(t)
	createdAt := time.Now().UTC().Add(-10 * time.Minute)
	plan, job, result, cache, payload := cacheFixtureAt(t, manifest, planEvent(nil), createdAt, createdAt.Add(time.Hour), createdAt)
	options := CacheReadOptions{
		Dependencies: artifactDependencies(),
		Toolchains:   artifactToolchains(),
		BuildFlags:   artifactBuildFlags(),
	}

	if err := ValidateCacheWrite(manifest, plan, result, cache, payload, time.Time{}); err != nil {
		t.Fatalf("ValidateCacheWrite() error = %v", err)
	}

	if err := ValidateCacheRead(manifest, plan, result, plan, job, cache, payload, options, time.Time{}); err != nil {
		t.Fatalf("ValidateCacheRead() error = %v", err)
	}
}

func TestCacheReadRejectsFutureProducerCompletion(t *testing.T) {
	manifest := loadManifest(t)

	tests := map[string]struct {
		completedAt time.Time
		want        string
	}{
		"within clock skew": {
			completedAt: p2TestNow.Add(maxPlanClockSkew),
		},
		"after clock skew": {
			completedAt: p2TestNow.Add(maxPlanClockSkew + time.Minute),
			want:        "completed_at",
		},
		"after plan expiry": {
			completedAt: p2TestNow.Add(3 * time.Hour),
			want:        "completed_at",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan, job, result, cache, payload := cacheFixture(t, manifest, planEvent(nil))
			setCacheResultCompletedAt(t, &cache, &result, test.completedAt)

			err := ValidateCacheRead(manifest, plan, result, plan, job, cache, payload, CacheReadOptions{
				Dependencies: artifactDependencies(),
				Toolchains:   artifactToolchains(),
				BuildFlags:   artifactBuildFlags(),
			}, p2TestNow)
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateCacheRead() error = %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCacheRead() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCacheWriteRejectsFutureProducerCompletion(t *testing.T) {
	manifest := loadManifest(t)

	tests := map[string]struct {
		completedAt time.Time
		want        string
	}{
		"within clock skew": {
			completedAt: p2TestNow.Add(maxPlanClockSkew),
		},
		"after clock skew": {
			completedAt: p2TestNow.Add(maxPlanClockSkew + time.Minute),
			want:        "completed_at",
		},
		"after plan expiry": {
			completedAt: p2TestNow.Add(3 * time.Hour),
			want:        "completed_at",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan, _, result, cache, payload := cacheFixture(t, manifest, planEvent(nil))
			setCacheResultCompletedAt(t, &cache, &result, test.completedAt)

			err := ValidateCacheWrite(manifest, plan, result, cache, payload, p2TestNow)
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateCacheWrite() error = %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCacheWrite() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCacheWriteRejectsKeyToolchainChecksumAndProducerDrift(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, payload := cacheFixture(t, manifest, planEvent(nil))

	tests := map[string]struct {
		editCache  func(*CacheManifest)
		editResult func(*ResultRecord)
		payload    []byte
		want       string
	}{
		"mismatched cache key": {
			editCache: func(cache *CacheManifest) {
				cache.CacheKey = cache.CacheKey + "-dreich"
			},
			want: "cache key",
		},
		"mismatched toolchain identity": {
			editCache: func(cache *CacheManifest) {
				cache.Toolchains[0].Digest = strings.Repeat("9", 64)
			},
			want: "cache key digest mismatch",
		},
		"checksum mismatch": {
			payload: []byte("different cache bytes\n"),
			want:    "checksum mismatch",
		},
		"path prefix conflict": {
			editCache: func(cache *CacheManifest) {
				cache.Files = []ArtifactFile{
					regularArtifactFile("go-build", "directory-looking-file\n", 0o644),
					regularArtifactFile("go-build-old", "neighbour\n", 0o644),
					regularArtifactFile("go-build/cache.db", "cache payload\n", 0o644),
				}
			},
			want: "conflicts with prefix path",
		},
		"symlink target missing from manifest": {
			editCache: func(cache *CacheManifest) {
				cache.Files = []ArtifactFile{
					symlinkArtifactFile("go-build/current", "cache.db", 0o777),
					regularArtifactFile("go-build/other.db", "other\n", 0o644),
				}
			},
			want: "not declared in the manifest",
		},
		"producer workflow path mismatch": {
			editCache: func(cache *CacheManifest) {
				cache.Provenance.Workflow = ".github/workflows/dev-release.yml"
			},
			want: "policy workflow",
		},
		"cache provenance key missing": {
			editCache: func(cache *CacheManifest) {
				cache.Provenance.CacheKey = ""
			},
			want: "cache provenance",
		},
		"producer workflow digest mismatch": {
			editCache: func(cache *CacheManifest) {
				cache.Provenance.WorkflowSHA256 = strings.Repeat("9", 64)
			},
			want: "policy digest",
		},
		"source commit mismatch": {
			editCache: func(cache *CacheManifest) {
				cache.Source.Commit = strings.Repeat("8", 40)
			},
			want: "cache key digest mismatch",
		},
		"producer timed out": {
			editCache: func(cache *CacheManifest) {
				cache.Provenance.ProducerStatus = "timed-out"
			},
			want: "timed-out",
		},
		"partial upload": {
			editCache: func(cache *CacheManifest) {
				cache.Provenance.UploadComplete = false
			},
			want: "upload is incomplete",
		},
		"provenance attempt mismatch": {
			editCache: func(cache *CacheManifest) {
				cache.Provenance.RunAttempt = 2
			},
			want: "run attempt",
		},
		"cancelled result": {
			editResult: func(result *ResultRecord) {
				setFinalOutcome(t, result, "cancelled", "cancelled", "")
			},
			want: "not success",
		},
		"stale result": {
			editResult: func(result *ResultRecord) {
				setFinalOutcome(t, result, "stale", "stale", "")
			},
			want: "not success",
		},
		"superseded result": {
			editResult: func(result *ResultRecord) {
				setFinalOutcome(t, result, "superseded", "superseded", strings.Repeat("5", 64))
			},
			want: "not success",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutatedCache := cache.copy()
			mutatedResult := result.copy()
			mutatedPayload := payload

			if test.payload != nil {
				mutatedPayload = test.payload
			}

			if test.editCache != nil {
				test.editCache(&mutatedCache)
				signCache(t, &mutatedCache)
			}

			if test.editResult != nil {
				test.editResult(&mutatedResult)
			}

			err := ValidateCacheWrite(manifest, plan, mutatedResult, mutatedCache, mutatedPayload, p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCacheWrite() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCacheWriteRejectsPayloadFileSetDrift(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, _ := cacheFixture(t, manifest, planEvent(nil))
	members := cachePayloadMembers()

	tests := map[string]struct {
		members []archiveMember
		want    string
	}{
		"extra cache member": {
			members: append(append([]archiveMember(nil), members...), archiveMember{name: "go-build/z-extra", data: "extra\n", mode: 0o644, typeflag: tar.TypeReg}),
			want:    "extra member",
		},
		"mode bits changed": {
			members: []archiveMember{
				{name: members[0].name, data: members[0].data, mode: 0o600, typeflag: tar.TypeReg},
				members[1],
			},
			want: "mode mismatch",
		},
		"payload checksum changed": {
			members: []archiveMember{
				{name: members[0].name, data: "cache paylood\n", mode: members[0].mode, typeflag: tar.TypeReg},
				members[1],
			},
			want: "checksum mismatch",
		},
		"symlink escapes": {
			members: []archiveMember{
				members[0],
				{name: members[1].name, linkTarget: "../outside", mode: members[1].mode, typeflag: tar.TypeSymlink},
			},
			want: "target mismatch",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutatedPayload := buildArtifactArchive(t, ArtifactFormatTar, test.members)
			mutatedCache := cache.copy()
			mutatedResult := result.copy()
			setCachePayloadDigest(t, &mutatedCache, &mutatedResult, mutatedPayload)

			err := ValidateCacheWrite(manifest, plan, mutatedResult, mutatedCache, mutatedPayload, p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCacheWrite() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCacheWriteRejectsTrailingPayloadAfterEndMarker(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, payload := cacheFixture(t, manifest, planEvent(nil))
	mutatedPayload := append(append([]byte(nil), payload...), []byte("dreich")...)
	mutatedCache := cache.copy()
	mutatedResult := result.copy()
	setCachePayloadDigest(t, &mutatedCache, &mutatedResult, mutatedPayload)

	err := ValidateCacheWrite(manifest, plan, mutatedResult, mutatedCache, mutatedPayload, p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("ValidateCacheWrite() error = %v, want trailing data rejection", err)
	}
}

func TestCacheWriteAcceptsZeroRecordPaddingAfterEndMarker(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, payload := cacheFixture(t, manifest, planEvent(nil))
	paddedPayload := append(append([]byte(nil), payload...), bytes.Repeat([]byte{0}, 16*512)...)
	paddedCache := cache.copy()
	paddedResult := result.copy()
	setCachePayloadDigest(t, &paddedCache, &paddedResult, paddedPayload)

	if err := ValidateCacheWrite(manifest, plan, paddedResult, paddedCache, paddedPayload, p2TestNow); err != nil {
		t.Fatalf("ValidateCacheWrite() error = %v", err)
	}
}

func TestCacheWriteRejectsMetadataBearingTarHeaders(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, _ := cacheFixture(t, manifest, planEvent(nil))
	members := cachePayloadMembers()
	members[0].paxRecords = map[string]string{"comment": "hidden metadata"}
	payload := buildArtifactArchive(t, ArtifactFormatTar, members)
	mutatedCache := cache.copy()
	mutatedResult := result.copy()
	setCachePayloadDigest(t, &mutatedCache, &mutatedResult, payload)

	err := ValidateCacheWrite(manifest, plan, mutatedResult, mutatedCache, payload, p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "PAX") {
		t.Fatalf("ValidateCacheWrite() error = %v, want PAX metadata rejection", err)
	}
}

func TestCacheWriteRejectsHiddenGNUExtensionRecords(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, _ := cacheFixture(t, manifest, planEvent(nil))
	longName := "go-build/cache-" + strings.Repeat("blether", 18) + ".db"
	members := []archiveMember{
		{name: longName, data: "cache payload\n", mode: 0o644, typeflag: tar.TypeReg, format: tar.FormatGNU},
		{name: "go-build/current", linkTarget: "cache.db", mode: 0o777, typeflag: tar.TypeSymlink},
	}
	payload := buildArtifactArchive(t, ArtifactFormatTar, members)
	mutatedCache := cache.copy()
	mutatedResult := result.copy()
	setCachePayloadDigest(t, &mutatedCache, &mutatedResult, payload)

	err := ValidateCacheWrite(manifest, plan, mutatedResult, mutatedCache, payload, p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "GNU long name") {
		t.Fatalf("ValidateCacheWrite() error = %v, want GNU extension rejection", err)
	}
}

func TestCacheWriteRejectsExcessiveZeroRecordPaddingAfterEndMarker(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, payload := cacheFixture(t, manifest, planEvent(nil))
	paddedPayload := append(append([]byte(nil), payload...), bytes.Repeat([]byte{0}, maxTarEOFPadding+512)...)
	paddedCache := cache.copy()
	paddedResult := result.copy()
	setCachePayloadDigest(t, &paddedCache, &paddedResult, paddedPayload)

	err := ValidateCacheWrite(manifest, plan, paddedResult, paddedCache, paddedPayload, p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "excessive trailing padding") {
		t.Fatalf("ValidateCacheWrite() error = %v, want excessive padding rejection", err)
	}
}

func TestCacheWriteChecksProvenanceBeforePayloadDecompression(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, payload := cacheFixture(t, manifest, planEvent(nil))
	paddedPayload := append(append([]byte(nil), payload...), bytes.Repeat([]byte{0}, maxTarEOFPadding+512)...)
	mutatedCache := cache.copy()
	mutatedResult := result.copy()
	setCachePayloadDigest(t, &mutatedCache, &mutatedResult, paddedPayload)
	mutatedCache.Provenance.CacheKey = "dreich"
	signCache(t, &mutatedCache)

	err := ValidateCacheWrite(manifest, plan, mutatedResult, mutatedCache, paddedPayload, p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "cache provenance") {
		t.Fatalf("ValidateCacheWrite() error = %v, want provenance rejection before payload", err)
	}
}

func TestCacheManifestRejectsDigestTamperingAndStrictDecodeDrift(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, payload := cacheFixture(t, manifest, planEvent(nil))

	t.Run("manifest digest tampered", func(t *testing.T) {
		mutatedCache := cache.copy()
		mutatedCache.TrustTier = "fork"

		err := ValidateCacheWrite(manifest, plan, result, mutatedCache, payload, p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "manifest digest mismatch") {
			t.Fatalf("ValidateCacheWrite() error = %v, want manifest digest mismatch", err)
		}
	})

	data, err := cache.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		data []byte
		want string
	}{
		"unknown field": {
			data: []byte(`{"schema_version":1,"blether":true}`),
			want: "unknown field",
		},
		"trailing value": {
			data: append(append([]byte(nil), data...), []byte("{}")...),
			want: "trailing JSON value",
		},
		"duplicate key": {
			data: []byte(`{"schema_version":1,"schema_version":1}`),
			want: "duplicate JSON object key",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeCacheManifest("cache.json", test.data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeCacheManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCacheProducerEntryPointsValidatePayloadAndFileRead(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, payload := cacheFixture(t, manifest, planEvent(nil))

	if err := ValidateCacheWrite(manifest, plan, result, cache, payload, p2TestNow); err != nil {
		t.Fatalf("ValidateCacheWrite() error = %v", err)
	}

	if err := VerifyCachePayload(cache, payload); err != nil {
		t.Fatalf("VerifyCachePayload() error = %v", err)
	}

	data, err := cache.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	read, err := ReadCacheManifest(path)
	if err != nil {
		t.Fatalf("ReadCacheManifest() error = %v", err)
	}

	if read.ManifestDigest != cache.ManifestDigest {
		t.Fatalf("ReadCacheManifest() digest = %s, want %s", read.ManifestDigest, cache.ManifestDigest)
	}
}

func TestCacheReadRejectsUntrustedWriteForTrustedConsumer(t *testing.T) {
	manifest := loadManifest(t)
	producerPlan, _, producerResult, cache, payload := cacheFixture(t, manifest, planEvent(nil))
	consumerPlan := buildTestPlan(t, manifest, planEvent(func(event *EventInput) {
		event.SameRepositoryAgent = false
		event.TrustedBase = true
	}), []string{"libghostty-native.lock.json"}, nil, true)
	consumerJob := planJobByMode(t, consumerPlan, "legacy/libghostty-native/native-gate")

	if producerPlan.TrustTier != "same-repository-agent" || consumerPlan.TrustTier != "trusted-base" {
		t.Fatalf("unexpected trust tiers producer=%s consumer=%s", producerPlan.TrustTier, consumerPlan.TrustTier)
	}

	err := ValidateCacheRead(manifest, producerPlan, producerResult, consumerPlan, consumerJob, cache, payload, CacheReadOptions{
		Dependencies: artifactDependencies(),
		Toolchains:   artifactToolchains(),
		BuildFlags:   artifactBuildFlags(),
	}, p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "cannot satisfy read tier") {
		t.Fatalf("ValidateCacheRead() error = %v, want trust-tier rejection", err)
	}
}

func TestCacheReadChecksTrustTierBeforePayloadVerification(t *testing.T) {
	manifest := loadManifest(t)
	producerPlan, _, producerResult, cache, _ := cacheFixture(t, manifest, planEvent(nil))
	consumerPlan := buildTestPlan(t, manifest, planEvent(func(event *EventInput) {
		event.SameRepositoryAgent = false
		event.TrustedBase = true
	}), []string{"libghostty-native.lock.json"}, nil, true)
	consumerJob := planJobByMode(t, consumerPlan, "legacy/libghostty-native/native-gate")
	malformedPayload := []byte("dreich cache bytes\n")

	err := ValidateCacheRead(manifest, producerPlan, producerResult, consumerPlan, consumerJob, cache, malformedPayload, CacheReadOptions{
		Dependencies: artifactDependencies(),
		Toolchains:   artifactToolchains(),
		BuildFlags:   artifactBuildFlags(),
	}, p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "cannot satisfy read tier") {
		t.Fatalf("ValidateCacheRead() error = %v, want trust-tier rejection before payload verification", err)
	}
}

func TestCacheReadChecksConsumerKeyBeforePayloadVerification(t *testing.T) {
	manifest := loadManifest(t)
	plan, _, result, cache, _ := cacheFixture(t, manifest, planEvent(func(event *EventInput) {
		event.SameRepositoryAgent = false
		event.TrustedBase = true
	}))
	consumerPlan := buildTestPlan(t, manifest, planEvent(func(event *EventInput) {
		event.SameRepositoryAgent = false
		event.TrustedBase = true
		event.Commit = strings.Repeat("9", 40)
	}), []string{"libghostty-native.lock.json"}, nil, true)
	consumerJob := planJobByMode(t, consumerPlan, "legacy/libghostty-native/native-gate")
	malformedPayload := []byte("thrawn cache bytes\n")

	err := ValidateCacheRead(manifest, plan, result, consumerPlan, consumerJob, cache, malformedPayload, CacheReadOptions{
		Dependencies: artifactDependencies(),
		Toolchains:   artifactToolchains(),
		BuildFlags:   artifactBuildFlags(),
	}, p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "consumer identity") {
		t.Fatalf("ValidateCacheRead() error = %v, want consumer key rejection before payload verification", err)
	}
}

func TestCacheReadRejectsConsumerIdentityMismatches(t *testing.T) {
	manifest := loadManifest(t)
	plan, job, result, cache, payload := cacheFixture(t, manifest, planEvent(func(event *EventInput) {
		event.SameRepositoryAgent = false
		event.TrustedBase = true
	}))

	tests := map[string]struct {
		consumerPlan RunPlan
		options      CacheReadOptions
		want         string
	}{
		"toolchain mismatch": {
			consumerPlan: plan,
			options: CacheReadOptions{
				Dependencies: artifactDependencies(),
				Toolchains: []IdentityDigest{
					{ID: "go", Version: "1.26.6", Digest: strings.Repeat("a", 64)},
					{ID: "zig", Version: "0.15.1", Digest: strings.Repeat("b", 64)},
				},
				BuildFlags: artifactBuildFlags(),
			},
			want: "consumer identity",
		},
		"source mismatch": {
			consumerPlan: buildTestPlan(t, manifest, planEvent(func(event *EventInput) {
				event.SameRepositoryAgent = false
				event.TrustedBase = true
				event.Commit = strings.Repeat("9", 40)
			}), []string{"libghostty-native.lock.json"}, nil, true),
			options: CacheReadOptions{
				Dependencies: artifactDependencies(),
				Toolchains:   artifactToolchains(),
				BuildFlags:   artifactBuildFlags(),
			},
			want: "consumer identity",
		},
		"event mismatch": {
			consumerPlan: buildTestPlan(t, manifest, workflowDispatchEvent(nil), []string{"libghostty-native.lock.json"}, nil, true),
			options: CacheReadOptions{
				Dependencies: artifactDependencies(),
				Toolchains:   artifactToolchains(),
				BuildFlags:   artifactBuildFlags(),
			},
			want: "event identity",
		},
		"build flag mismatch": {
			consumerPlan: plan,
			options: CacheReadOptions{
				Dependencies: artifactDependencies(),
				Toolchains:   artifactToolchains(),
				BuildFlags: []BuildFlag{
					{Name: "cgo_enabled", Value: "0"},
					{Name: "tags", Value: "libghostty"},
				},
			},
			want: "consumer identity",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			consumerJob := planJobByMode(t, test.consumerPlan, "legacy/libghostty-native/native-gate")

			err := ValidateCacheRead(manifest, plan, result, test.consumerPlan, consumerJob, cache, payload, test.options, p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCacheRead() error = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("consumer job absent from plan", func(t *testing.T) {
		missingJob := job
		missingJob.Coordinate = "missing"

		err := ValidateCacheRead(manifest, plan, result, plan, missingJob, cache, payload, CacheReadOptions{
			Dependencies: artifactDependencies(),
			Toolchains:   artifactToolchains(),
			BuildFlags:   artifactBuildFlags(),
		}, p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "not in the consumer plan") {
			t.Fatalf("ValidateCacheRead() error = %v, want missing consumer job rejection", err)
		}
	})
}

func cacheFixture(t *testing.T, manifest Manifest, event EventInput) (RunPlan, PlanJob, ResultRecord, CacheManifest, []byte) {
	t.Helper()

	return cacheFixtureAt(t, manifest, event, p2TestNow, p2TestNow.Add(time.Hour), p2TestNow)
}

func cacheFixtureAt(t *testing.T, manifest Manifest, event EventInput, createdAt, expiresAt, now time.Time) (RunPlan, PlanJob, ResultRecord, CacheManifest, []byte) {
	t.Helper()

	files := cachePayloadFiles()
	payload := buildArtifactArchive(t, ArtifactFormatTar, cachePayloadMembers())
	cacheDigest := sha256Hex(payload)
	dependencies := artifactDependencies()
	toolchains := artifactToolchains()
	buildFlags := artifactBuildFlags()
	plan := buildTestPlanAt(t, manifest, event, createdAt, expiresAt, now)
	job := planJobByMode(t, plan, "legacy/libghostty-native/native-gate")
	attempt := resultAttempt(1, "success", "", now)
	attempt.CacheDigest = cacheDigest

	result, err := NewResultRecord(plan, job, []ResultAttempt{attempt})
	if err != nil {
		t.Fatal(err)
	}

	cache, err := NewCacheManifest(manifest, plan, result, CacheManifestInput{
		CacheFormat:  ArtifactFormatTar,
		CacheDigest:  cacheDigest,
		Dependencies: dependencies,
		Toolchains:   toolchains,
		BuildFlags:   buildFlags,
		Files:        files,
		Provenance: CacheProvenance{
			Workflow:       testProducerWorkflow,
			WorkflowSHA256: testProducerWorkflowSHA256,
			RunID:          testProducerRunID,
			RunAttempt:     1,
			JobID:          "native-gate",
			JobName:        "Native backend gate",
			ProducerStatus: "success",
			UploadComplete: true,
			CacheKey:       cacheKeyForResult(t, manifest, result, dependencies, toolchains, buildFlags),
			CacheDigest:    cacheDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return plan, job, result, cache, payload
}

func cacheKeyForResult(t *testing.T, manifest Manifest, result ResultRecord, dependencies []IdentityDigest, toolchains []IdentityDigest, buildFlags []BuildFlag) string {
	t.Helper()

	platform, ok := platformByID(manifest, result.Platform)
	if !ok {
		t.Fatalf("missing platform %s", result.Platform)
	}

	digest, err := CacheKeyDigest(CacheKeyMaterial{
		SchemaVersion: CacheSchemaVersion,
		PolicyVersion: result.PolicyVersion,
		PolicyDigest:  result.PolicyDigest,
		Source:        result.Source,
		Mode:          result.Mode,
		Coordinate:    result.Coordinate,
		Capability:    result.Capability,
		Platform:      result.Platform,
		OS:            platform.OS,
		Architecture:  platform.Architecture,
		Dependencies:  dependencies,
		Toolchains:    toolchains,
		BuildFlags:    buildFlags,
	})
	if err != nil {
		t.Fatal(err)
	}

	return cacheKeyPrefix + digest
}

func cachePayloadFiles() []ArtifactFile {
	return []ArtifactFile{
		regularArtifactFile("go-build/cache.db", "cache payload\n", 0o644),
		symlinkArtifactFile("go-build/current", "cache.db", 0o777),
	}
}

func cachePayloadMembers() []archiveMember {
	return []archiveMember{
		{name: "go-build/cache.db", data: "cache payload\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "go-build/current", linkTarget: "cache.db", mode: 0o777, typeflag: tar.TypeSymlink},
	}
}

func setCachePayloadDigest(t *testing.T, cache *CacheManifest, result *ResultRecord, payload []byte) {
	t.Helper()

	digest := sha256Hex(payload)
	cache.CacheDigest = digest
	cache.Provenance.CacheDigest = digest
	result.Attempts[len(result.Attempts)-1].CacheDigest = digest
	result.CacheDigest = digest
	signResult(t, result)
	cache.ResultDigest = result.ResultDigest
	signCache(t, cache)
}

func setCacheResultCompletedAt(t *testing.T, cache *CacheManifest, result *ResultRecord, completedAt time.Time) {
	t.Helper()

	setResultCompletedAt(t, result, completedAt)
	cache.ResultDigest = result.ResultDigest
	signCache(t, cache)
}

func signCache(t *testing.T, cache *CacheManifest) {
	t.Helper()

	digest, err := cache.Digest()
	if err != nil {
		t.Fatal(err)
	}

	cache.ManifestDigest = digest
}
