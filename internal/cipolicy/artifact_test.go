package cipolicy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type archiveMember struct {
	name       string
	data       string
	mode       int64
	forceSize  int64
	linkTarget string
	typeflag   byte
	format     tar.Format
	accessTime time.Time
	paxRecords map[string]string
	xattrs     map[string]string
}

const testProducerWorkflow = ".github/workflows/libghostty-native.yml"

// Regenerate with: sha256sum .github/workflows/libghostty-native.yml
const testProducerWorkflowSHA256 = "b3bbea2331530670e031afbea601e21fd9512ccc84c7e7a7a9b3bc3a7473f2ed"

const testProducerRunID = int64(30152132020)

func TestArtifactConsumerVerifiesNativeAndReleaseShapes(t *testing.T) {
	manifest := loadManifest(t)

	tests := map[string]struct {
		artifactType string
		artifactID   string
		format       string
		workflow     string
		files        []ArtifactFile
		members      []archiveMember
	}{
		"native libghostty testing artifact": {
			artifactType: ArtifactTypeNativeLibghostty,
			artifactID:   "gr-libghostty-linux-amd64",
			format:       ArtifactFormatTar,
			workflow:     testProducerWorkflow,
			files: []ArtifactFile{
				regularArtifactFile("THIRD_PARTY_NOTICES.libghostty.md", "notice\n", 0o644),
				regularArtifactFile("gr-linux-amd64", "native-binary\n", 0o755),
				regularArtifactFile("libghostty-native.spdx.json", `{"spdx":"braw"}`+"\n", 0o644),
			},
			members: []archiveMember{
				{name: "THIRD_PARTY_NOTICES.libghostty.md", data: "notice\n", mode: 0o644, typeflag: tar.TypeReg},
				{name: "gr-linux-amd64", data: "native-binary\n", mode: 0o755, typeflag: tar.TypeReg},
				{name: "libghostty-native.spdx.json", data: `{"spdx":"braw"}` + "\n", mode: 0o644, typeflag: tar.TypeReg},
			},
		},
		"release shaped artifact": {
			artifactType: ArtifactTypeRelease,
			artifactID:   "graith-dev-linux-amd64",
			format:       ArtifactFormatTarGzip,
			workflow:     testProducerWorkflow,
			files: []ArtifactFile{
				regularArtifactFile("LICENSE", "license\n", 0o644),
				regularArtifactFile("README.md", "readme\n", 0o644),
				symlinkArtifactFile("bin/current", "gr-dev", 0o777),
				regularArtifactFile("bin/gr-dev", "release-binary\n", 0o755),
			},
			members: []archiveMember{
				{name: "LICENSE", data: "license\n", mode: 0o644, typeflag: tar.TypeReg},
				{name: "README.md", data: "readme\n", mode: 0o644, typeflag: tar.TypeReg},
				{name: "bin/current", linkTarget: "gr-dev", mode: 0o777, typeflag: tar.TypeSymlink},
				{name: "bin/gr-dev", data: "release-binary\n", mode: 0o755, typeflag: tar.TypeReg},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan, result, artifact, archive := artifactFixture(t, manifest, test.artifactType, test.artifactID, test.format, test.workflow, test.files, test.members)

			options := artifactConsumerOptions(plan, artifact, test.artifactType, test.artifactID)
			options.Workflow = test.workflow
			options.RunID = artifact.Provenance.RunID
			options.RunAttempt = artifact.Provenance.RunAttempt

			if err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, options, p2TestNow); err != nil {
				t.Fatal(err)
			}

			output := filepath.Join(t.TempDir(), "out")
			if err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, output, artifactConsumerOptions(plan, artifact, test.artifactType, test.artifactID), p2TestNow); err != nil {
				t.Fatal(err)
			}

			assertExtractedArtifactFiles(t, output, test.files)
		})
	}
}

func TestArtifactConsumerAcceptsExpiredProducerPlanForCurrentConsumer(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	archive := buildArtifactArchive(t, ArtifactFormatTar, members)
	archiveDigest := sha256Hex(archive)
	event := planEvent(nil)
	producerTime := p2TestNow.Add(-2 * time.Hour)
	producerPlan := buildTestPlanAt(t, manifest, event, producerTime, producerTime.Add(time.Hour), producerTime)
	producerJob := planJobByMode(t, producerPlan, "legacy/libghostty-native/native-gate")
	attempt := resultAttempt(1, "success", "", producerTime.Add(15*time.Minute))
	attempt.ArtifactDigest = archiveDigest

	result, err := NewArtifactProducerResult(producerPlan, producerJob, []ArtifactProducerAttempt{attempt})
	if err != nil {
		t.Fatal(err)
	}

	artifact, err := NewArtifactManifest(manifest, producerPlan, result, ArtifactManifestInput{
		ArtifactType:   ArtifactTypeRelease,
		ArtifactID:     "graith-dev-linux-amd64",
		ArtifactFormat: ArtifactFormatTar,
		ArtifactDigest: archiveDigest,
		Dependencies:   artifactDependencies(),
		Toolchains:     artifactToolchains(),
		BuildFlags:     artifactBuildFlags(),
		Files:          files,
		Provenance: ArtifactProvenance{
			Workflow:       testProducerWorkflow,
			WorkflowSHA256: testProducerWorkflowSHA256,
			RunID:          testProducerRunID,
			RunAttempt:     1,
			JobID:          "native-gate",
			JobName:        "Native backend gate",
			ProducerStatus: "success",
			UploadComplete: true,
			ArtifactID:     "graith-dev-linux-amd64",
			ArtifactDigest: archiveDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	consumerPlan := buildTestPlanAt(t, manifest, event, p2TestNow, p2TestNow.Add(time.Hour), p2TestNow)
	options := artifactConsumerOptions(consumerPlan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64")

	if err := VerifyArtifactConsumer(manifest, producerPlan, result, artifact, archive, options, p2TestNow); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactConsumerZeroNowUsesCurrentTime(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	archive := buildArtifactArchive(t, ArtifactFormatTar, members)
	archiveDigest := sha256Hex(archive)
	event := planEvent(nil)
	createdAt := time.Now().UTC().Add(-5 * time.Minute)
	plan := buildTestPlanAt(t, manifest, event, createdAt, createdAt.Add(time.Hour), createdAt)
	job := planJobByMode(t, plan, "legacy/libghostty-native/native-gate")
	attempt := resultAttempt(1, "success", "", createdAt.Add(time.Minute))
	attempt.ArtifactDigest = archiveDigest

	result, err := NewArtifactProducerResult(plan, job, []ArtifactProducerAttempt{attempt})
	if err != nil {
		t.Fatal(err)
	}

	artifact, err := NewArtifactManifest(manifest, plan, result, ArtifactManifestInput{
		ArtifactType:   ArtifactTypeRelease,
		ArtifactID:     "graith-dev-linux-amd64",
		ArtifactFormat: ArtifactFormatTar,
		ArtifactDigest: archiveDigest,
		Dependencies:   artifactDependencies(),
		Toolchains:     artifactToolchains(),
		BuildFlags:     artifactBuildFlags(),
		Files:          files,
		Provenance: ArtifactProvenance{
			Workflow:       testProducerWorkflow,
			WorkflowSHA256: testProducerWorkflowSHA256,
			RunID:          testProducerRunID,
			RunAttempt:     1,
			JobID:          "native-gate",
			JobName:        "Native backend gate",
			ProducerStatus: "success",
			UploadComplete: true,
			ArtifactID:     "graith-dev-linux-amd64",
			ArtifactDigest: archiveDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactConsumerRejectsFutureProducerCompletion(t *testing.T) {
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
			files := releaseArtifactFiles()
			members := releaseArchiveMembers()
			plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
			setArtifactResultCompletedAt(t, &artifact, &result, test.completedAt)
			options := artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64")

			err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, options, p2TestNow)
			if test.want == "" {
				if err != nil {
					t.Fatalf("VerifyArtifactConsumer() error = %v", err)
				}

				output := filepath.Join(t.TempDir(), "out")
				if err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, output, options, p2TestNow); err != nil {
					t.Fatalf("ExtractVerifiedArtifact() error = %v", err)
				}

				assertExtractedArtifactFiles(t, output, files)

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyArtifactConsumer() error = %v, want %q", err, test.want)
			}

			err = ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, filepath.Join(t.TempDir(), "out"), options, p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ExtractVerifiedArtifact() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactProducerRejectsFutureCompletion(t *testing.T) {
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
			files := releaseArtifactFiles()
			members := releaseArchiveMembers()
			plan, result, artifact, _ := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
			setArtifactResultCompletedAt(t, &artifact, &result, test.completedAt)

			err := ValidateArtifactManifest(manifest, plan, result, artifact, p2TestNow)
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateArtifactManifest() error = %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateArtifactManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactConsumerAcceptsParentRelativeSymlinkWithinManifest(t *testing.T) {
	manifest := loadManifest(t)
	files := []ArtifactFile{
		symlinkArtifactFile("bin/gr", "../libexec/gr", 0o777),
		regularArtifactFile("libexec/gr", "binary\n", 0o755),
	}
	members := []archiveMember{
		{name: "bin/gr", linkTarget: "../libexec/gr", mode: 0o777, typeflag: tar.TypeSymlink},
		{name: "libexec/gr", data: "binary\n", mode: 0o755, typeflag: tar.TypeReg},
	}
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	if err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "out")
	if err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, output, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow); err != nil {
		t.Fatal(err)
	}

	assertExtractedArtifactFiles(t, output, files)
}

func TestArtifactProducerEntryPointsValidateManifestArchiveAndFileRead(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	if err := ValidateArtifactManifest(manifest, plan, result, artifact, p2TestNow); err != nil {
		t.Fatalf("ValidateArtifactManifest() error = %v", err)
	}

	if err := VerifyArtifactArchive(artifact, archive); err != nil {
		t.Fatalf("VerifyArtifactArchive() error = %v", err)
	}

	data, err := artifact.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	read, err := ReadArtifactManifest(path)
	if err != nil {
		t.Fatalf("ReadArtifactManifest() error = %v", err)
	}

	if read.ManifestDigest != artifact.ManifestDigest {
		t.Fatalf("ReadArtifactManifest() digest = %s, want %s", read.ManifestDigest, artifact.ManifestDigest)
	}
}

func TestArtifactProducerResultValidationRejectsTampering(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, _ := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	tests := map[string]struct {
		edit   func(*ArtifactProducerResult)
		resign bool
		want   string
	}{
		"unsupported schema version": {
			edit: func(result *ArtifactProducerResult) {
				result.SchemaVersion = 0
			},
			resign: true,
			want:   "unsupported artifact producer result schema version",
		},
		"result digest mismatch": {
			edit: func(result *ArtifactProducerResult) {
				result.ResultDigest = strings.Repeat("c", 64)
			},
			want: "artifact producer result digest mismatch",
		},
		"stale plan identity": {
			edit: func(result *ArtifactProducerResult) {
				result.PlanDigest = strings.Repeat("c", 64)
			},
			resign: true,
			want:   "stale artifact producer result binding",
		},
		"coordinate identity mismatch": {
			edit: func(result *ArtifactProducerResult) {
				result.Capability = "docs-preview"
			},
			resign: true,
			want:   "does not match plan coordinate identity",
		},
		"no attempts": {
			edit: func(result *ArtifactProducerResult) {
				result.Attempts = nil
			},
			resign: true,
			want:   "has no attempts",
		},
		"attempt history is not contiguous": {
			edit: func(result *ArtifactProducerResult) {
				result.Attempts[0].Attempt = 2
			},
			resign: true,
			want:   "attempt history is not contiguous",
		},
		"invalid attempt timestamp": {
			edit: func(result *ArtifactProducerResult) {
				result.Attempts[0].StartedAt = result.Attempts[0].CompletedAt.Add(time.Minute)
			},
			resign: true,
			want:   "attempt 1 has invalid timestamps",
		},
		"commit-shaped evidence digest": {
			edit: func(result *ArtifactProducerResult) {
				result.Attempts[0].EvidenceDigest = strings.Repeat("a", 40)
			},
			resign: true,
			want:   "evidence digest",
		},
		"first outcome drift": {
			edit: func(result *ArtifactProducerResult) {
				result.FirstStatus = "failed"
				result.FirstFailureClass = "runner"
			},
			resign: true,
			want:   "does not preserve first attempt outcome",
		},
		"final outcome drift": {
			edit: func(result *ArtifactProducerResult) {
				result.Status = "failed"
				result.FailureClass = "runner"
			},
			resign: true,
			want:   "final outcome does not match final attempt",
		},
		"aggregate timestamp drift": {
			edit: func(result *ArtifactProducerResult) {
				result.StartedAt = result.StartedAt.Add(time.Second)
			},
			resign: true,
			want:   "does not bind aggregate timestamps to attempts",
		},
		"aggregate digest drift": {
			edit: func(result *ArtifactProducerResult) {
				result.EvidenceDigest = strings.Repeat("c", 64)
			},
			resign: true,
			want:   "aggregate digests do not match final attempt",
		},
		"supersession identity must be a result digest": {
			edit: func(result *ArtifactProducerResult) {
				setFinalOutcome(t, result, "superseded", "stale", strings.Repeat("1", 40))
			},
			want: "superseded artifact producer result requires a supersession identity",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := result.copy()
			test.edit(&mutated)

			if test.resign {
				signResult(t, &mutated)
			}

			err := ValidateArtifactManifest(manifest, plan, mutated, artifact, p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateArtifactManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactArchiveRejectsExactMemberDrift(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, _ := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	tests := map[string]struct {
		members []archiveMember
		want    string
	}{
		"extra member": {
			members: append(append([]archiveMember(nil), members...), archiveMember{name: "z-extra", data: "extra\n", mode: 0o644, typeflag: tar.TypeReg}),
			want:    "extra member",
		},
		"missing member": {
			members: members[:len(members)-1],
			want:    "missing",
		},
		"archive order changed": {
			members: []archiveMember{members[1], members[0], members[2], members[3]},
			want:    "order mismatch",
		},
		"mode bits changed": {
			members: []archiveMember{
				members[0],
				{name: members[1].name, data: members[1].data, mode: 0o600, typeflag: tar.TypeReg},
				members[2],
				members[3],
			},
			want: "mode mismatch",
		},
		"line endings changed": {
			members: []archiveMember{
				members[0],
				{name: members[1].name, data: "readm\r\n", mode: members[1].mode, typeflag: tar.TypeReg},
				members[2],
				members[3],
			},
			want: "checksum mismatch",
		},
		"symlink escapes": {
			members: []archiveMember{
				members[0],
				members[1],
				{name: members[2].name, linkTarget: "../outside", mode: 0o777, typeflag: tar.TypeSymlink},
				members[3],
			},
			want: "target is invalid",
		},
		"symlink has body size": {
			members: []archiveMember{
				members[0],
				members[1],
				{name: members[2].name, linkTarget: "gr-dev", mode: 0o777, forceSize: 7, typeflag: tar.TypeSymlink},
				members[3],
			},
			want: "archive size",
		},
		"hardlink is rejected": {
			members: []archiveMember{
				members[0],
				members[1],
				members[2],
				{name: members[3].name, linkTarget: "/tmp/outside", mode: members[3].mode, typeflag: tar.TypeLink},
			},
			want: "hardlink",
		},
		"absolute archive path": {
			members: []archiveMember{
				{name: "/tmp/gr-dev", data: "release-binary\n", mode: 0o755, typeflag: tar.TypeReg},
				members[1],
				members[2],
				members[3],
			},
			want: "absolute path",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutatedArchive := buildArtifactArchive(t, ArtifactFormatTar, test.members)
			mutatedArtifact := artifact.copy()
			mutatedResult := result.copy()
			setArtifactArchiveDigest(t, &mutatedArtifact, &mutatedResult, mutatedArchive)

			err := VerifyArtifactConsumer(manifest, plan, mutatedResult, mutatedArtifact, mutatedArchive, artifactConsumerOptions(plan, mutatedArtifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyArtifactConsumer() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactArchiveRejectsTrailingPayloadAfterEndMarker(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()

	tests := map[string]struct {
		format string
		mutate func([]byte) []byte
		want   string
	}{
		"tar trailing bytes": {
			format: ArtifactFormatTar,
			mutate: func(archive []byte) []byte {
				return append(append([]byte(nil), archive...), []byte("dreich")...)
			},
			want: "trailing data",
		},
		"gzip member has tar trailing bytes": {
			format: ArtifactFormatTarGzip,
			mutate: func([]byte) []byte {
				tarPayload := buildArtifactArchive(t, ArtifactFormatTar, members)
				tarPayload = append(tarPayload, []byte("dreich")...)

				return gzipPayload(t, tarPayload)
			},
			want: "trailing data",
		},
		"gzip stream has trailing bytes": {
			format: ArtifactFormatTarGzip,
			mutate: func(archive []byte) []byte {
				return append(append([]byte(nil), archive...), []byte("dreich")...)
			},
			want: "trailing data",
		},
		"gzip stream has concatenated member": {
			format: ArtifactFormatTarGzip,
			mutate: func(archive []byte) []byte {
				return append(append([]byte(nil), archive...), gzipPayload(t, []byte("dreich"))...)
			},
			want: "trailing data",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", test.format, testProducerWorkflow, files, members)
			mutatedArchive := test.mutate(archive)
			setArtifactArchiveDigest(t, &artifact, &result, mutatedArchive)

			err := VerifyArtifactConsumer(manifest, plan, result, artifact, mutatedArchive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyArtifactConsumer() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactArchiveAcceptsZeroRecordPaddingAfterEndMarker(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	padding := bytes.Repeat([]byte{0}, 16*512)

	tests := map[string]struct {
		format string
		build  func([]byte) []byte
	}{
		"tar": {
			format: ArtifactFormatTar,
			build: func(archive []byte) []byte {
				return append(append([]byte(nil), archive...), padding...)
			},
		},
		"tar gzip": {
			format: ArtifactFormatTarGzip,
			build: func([]byte) []byte {
				tarPayload := buildArtifactArchive(t, ArtifactFormatTar, members)
				tarPayload = append(tarPayload, padding...)

				return gzipPayload(t, tarPayload)
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", test.format, testProducerWorkflow, files, members)
			paddedArchive := test.build(archive)
			setArtifactArchiveDigest(t, &artifact, &result, paddedArchive)

			err := VerifyArtifactConsumer(manifest, plan, result, artifact, paddedArchive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
			if err != nil {
				t.Fatalf("VerifyArtifactConsumer() error = %v", err)
			}
		})
	}
}

func TestArtifactArchiveRejectsMetadataBearingTarHeaders(t *testing.T) {
	manifest := loadManifest(t)
	files := []ArtifactFile{
		regularArtifactFile("file.txt", "braw\n", 0o644),
	}

	tests := map[string]struct {
		member archiveMember
		want   string
	}{
		"pax records": {
			member: archiveMember{
				name:       "file.txt",
				data:       "braw\n",
				mode:       0o644,
				typeflag:   tar.TypeReg,
				paxRecords: map[string]string{"comment": "hidden metadata"},
			},
			want: "PAX",
		},
		"xattrs": {
			member: archiveMember{
				name:     "file.txt",
				data:     "braw\n",
				mode:     0o644,
				typeflag: tar.TypeReg,
				xattrs:   map[string]string{"user.hidden": "metadata"},
			},
			want: "PAX",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			members := []archiveMember{test.member}
			plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

			err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyArtifactConsumer() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactArchiveRejectsHiddenGNUExtensionRecords(t *testing.T) {
	manifest := loadManifest(t)
	longName := "braw-" + strings.Repeat("canny", 24) + ".txt"
	longTarget := "target-" + strings.Repeat("dreich", 18) + ".txt"

	tests := map[string]struct {
		files   []ArtifactFile
		members []archiveMember
		want    string
	}{
		"long name": {
			files: []ArtifactFile{
				regularArtifactFile(longName, "braw\n", 0o644),
			},
			members: []archiveMember{
				{name: longName, data: "braw\n", mode: 0o644, typeflag: tar.TypeReg, format: tar.FormatGNU},
			},
			want: "GNU long name",
		},
		"long link": {
			files: []ArtifactFile{
				symlinkArtifactFile("current", longTarget, 0o777),
				regularArtifactFile(longTarget, "target\n", 0o644),
			},
			members: []archiveMember{
				{name: "current", linkTarget: longTarget, mode: 0o777, typeflag: tar.TypeSymlink, format: tar.FormatGNU},
				{name: longTarget, data: "target\n", mode: 0o644, typeflag: tar.TypeReg, format: tar.FormatGNU},
			},
			want: "GNU long link",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, test.files, test.members)

			err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyArtifactConsumer() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactArchiveAcceptsGNUShortNameWithAccessTime(t *testing.T) {
	manifest := loadManifest(t)
	files := []ArtifactFile{
		regularArtifactFile("file.txt", "braw\n", 0o644),
	}
	members := []archiveMember{
		{
			name:       "file.txt",
			data:       "braw\n",
			mode:       0o644,
			typeflag:   tar.TypeReg,
			format:     tar.FormatGNU,
			accessTime: p2TestNow.Add(-time.Hour),
		},
	}
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	if err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow); err != nil {
		t.Fatalf("VerifyArtifactConsumer() error = %v", err)
	}
}

func TestArtifactArchiveAcceptsUSTARPrefixPath(t *testing.T) {
	manifest := loadManifest(t)
	name := strings.Repeat("croft/", 20) + "gr"
	files := []ArtifactFile{
		regularArtifactFile(name, "braw\n", 0o644),
	}
	members := []archiveMember{
		{name: name, data: "braw\n", mode: 0o644, typeflag: tar.TypeReg, format: tar.FormatUSTAR},
	}
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	if err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow); err != nil {
		t.Fatalf("VerifyArtifactConsumer() error = %v", err)
	}
}

func TestArtifactVerifyOnlyDoesNotCollectRegularPayloadData(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	_, _, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	verifyEntries, err := verifiedArtifactEntries(artifact, archive, false)
	if err != nil {
		t.Fatalf("verifiedArtifactEntries(false) error = %v", err)
	}

	for _, entry := range verifyEntries {
		if entry.file.Kind == ArtifactFileRegular && len(entry.data) != 0 {
			t.Fatalf("verify-only entry %s collected %d bytes, want none", entry.file.Path, len(entry.data))
		}
	}

	extractEntries, err := verifiedArtifactEntries(artifact, archive, true)
	if err != nil {
		t.Fatalf("verifiedArtifactEntries(true) error = %v", err)
	}

	for _, entry := range extractEntries {
		if entry.file.Kind == ArtifactFileRegular && len(entry.data) != int(entry.file.Size) {
			t.Fatalf("extraction entry %s collected %d bytes, want %d", entry.file.Path, len(entry.data), entry.file.Size)
		}
	}
}

func TestArtifactArchiveRejectsDirectoryMembers(t *testing.T) {
	manifest := loadManifest(t)
	files := []ArtifactFile{
		regularArtifactFile("bin", "", 0o755),
	}
	members := []archiveMember{
		{name: "bin", mode: 0o755, typeflag: tar.TypeDir},
	}
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("VerifyArtifactConsumer() error = %v, want directory member rejection", err)
	}
}

func TestArtifactArchiveRejectsExcessiveZeroRecordPaddingAfterEndMarker(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	padding := bytes.Repeat([]byte{0}, maxTarEOFPadding+512)

	tests := map[string]struct {
		format string
		build  func([]byte) []byte
	}{
		"tar": {
			format: ArtifactFormatTar,
			build: func(archive []byte) []byte {
				return append(append([]byte(nil), archive...), padding...)
			},
		},
		"tar gzip": {
			format: ArtifactFormatTarGzip,
			build: func([]byte) []byte {
				tarPayload := buildArtifactArchive(t, ArtifactFormatTar, members)
				tarPayload = append(tarPayload, padding...)

				return gzipPayload(t, tarPayload)
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", test.format, testProducerWorkflow, files, members)
			paddedArchive := test.build(archive)
			setArtifactArchiveDigest(t, &artifact, &result, paddedArchive)

			err := VerifyArtifactConsumer(manifest, plan, result, artifact, paddedArchive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
			if err == nil || !strings.Contains(err.Error(), "excessive trailing padding") {
				t.Fatalf("VerifyArtifactConsumer() error = %v, want excessive padding rejection", err)
			}
		})
	}
}

func TestArtifactManifestRejectsUnsafeStaleAndIncompleteBindings(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	tests := map[string]struct {
		editArtifact func(*ArtifactContractManifest)
		editResult   func(*ArtifactProducerResult)
		want         string
	}{
		"absolute manifest path": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Files[0].Path = "/tmp/gr-dev"
			},
			want: "absolute path",
		},
		"traversal manifest path": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Files[0].Path = "../gr-dev"
			},
			want: "traverses",
		},
		"duplicate manifest path": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Files = append(artifact.Files, artifact.Files[0])
			},
			want: "duplicate path",
		},
		"source commit mismatch": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Source.Commit = strings.Repeat("9", 40)
			},
			want: "plan identity",
		},
		"policy mismatch": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.PolicyDigest = strings.Repeat("8", 64)
			},
			want: "policy identity",
		},
		"result mismatch": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.ResultDigest = strings.Repeat("7", 64)
			},
			want: "result identity",
		},
		"provenance run missing": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Provenance.RunID = 0
			},
			want: "run ID",
		},
		"producer timed out": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Provenance.ProducerStatus = "timed-out"
			},
			want: "timed-out",
		},
		"partial upload": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Provenance.UploadComplete = false
			},
			want: "upload is incomplete",
		},
		"provenance attempt mismatch": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Provenance.RunAttempt = 2
			},
			want: "run attempt",
		},
		"cancelled result": {
			editResult: func(result *ArtifactProducerResult) {
				setFinalOutcome(t, result, "cancelled", "cancelled", "")
			},
			want: "not success",
		},
		"stale result": {
			editResult: func(result *ArtifactProducerResult) {
				setFinalOutcome(t, result, "stale", "stale", "")
			},
			want: "not success",
		},
		"superseded result": {
			editResult: func(result *ArtifactProducerResult) {
				setFinalOutcome(t, result, "superseded", "superseded", strings.Repeat("5", 64))
			},
			want: "not success",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutatedArtifact := artifact.copy()
			mutatedResult := result.copy()

			if test.editArtifact != nil {
				test.editArtifact(&mutatedArtifact)
				signArtifact(t, &mutatedArtifact)
			}

			if test.editResult != nil {
				test.editResult(&mutatedResult)
			}

			err := VerifyArtifactConsumer(manifest, plan, mutatedResult, mutatedArtifact, archive, artifactConsumerOptions(plan, mutatedArtifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyArtifactConsumer() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactConsumerRejectsMissingCrossTierAndAbsentJobBinding(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	t.Run("missing consumer expectations", func(t *testing.T) {
		err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, ArtifactVerificationOptions{}, p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "expected artifact type") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want missing expected artifact type", err)
		}
	})

	t.Run("same repository artifact cannot satisfy trusted consumer", func(t *testing.T) {
		consumerPlan := buildTestPlan(t, manifest, planEvent(func(event *EventInput) {
			event.SameRepositoryAgent = false
			event.TrustedBase = true
		}), []string{"libghostty-native.lock.json"}, nil, true)

		err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, artifactConsumerOptions(consumerPlan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "cannot satisfy consumer tier") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want trust-tier rejection", err)
		}
	})

	t.Run("consumer job is required", func(t *testing.T) {
		options := artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64")
		options.ConsumerJob = PlanJob{}

		err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, options, p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "consumer job is required") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want missing consumer job rejection", err)
		}
	})

	t.Run("consumer job absent from plan", func(t *testing.T) {
		missingJob := planJobByMode(t, plan, "legacy/libghostty-native/native-gate")
		missingJob.Coordinate = "missing"
		options := artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64")
		options.ConsumerJob = missingJob

		err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, options, p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "not in the consumer plan") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want missing consumer job rejection", err)
		}
	})

	t.Run("consumer job evidence refs must match plan", func(t *testing.T) {
		options := artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64")
		options.ConsumerJob.EvidenceRefs = []string{"p0-inventory:" + strings.Repeat("a", 64) + "#ci/braw"}

		err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, options, p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "not in the consumer plan") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want consumer evidence ref binding rejection", err)
		}
	})

	t.Run("producer coordinate must match expected", func(t *testing.T) {
		options := artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64")
		options.ProducerCoordinate = "missing"

		err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, options, p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "producer coordinate") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want producer coordinate rejection", err)
		}
	})

	t.Run("run ID is required", func(t *testing.T) {
		options := artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64")
		options.RunID = 0

		err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, options, p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "expected run ID") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want missing run ID rejection", err)
		}
	})
}

func TestArtifactManifestRejectsPolicyTraceAndDigestTampering(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	tests := map[string]struct {
		editArtifact func(*ArtifactContractManifest)
		sign         bool
		want         string
	}{
		"workflow path not declared by policy": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Provenance.Workflow = ".github/workflows/dev-release.yml"
			},
			sign: true,
			want: "policy workflow",
		},
		"workflow digest not declared by policy": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Provenance.WorkflowSHA256 = strings.Repeat("9", 64)
			},
			sign: true,
			want: "policy digest",
		},
		"manifest digest tampered": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.TrustTier = "fork"
			},
			want: "manifest digest mismatch",
		},
		"path prefix conflict": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Files = []ArtifactFile{
					regularArtifactFile("bin", "directory-looking-file\n", 0o644),
					regularArtifactFile("bin-old", "neighbour\n", 0o644),
					regularArtifactFile("bin/gr", "binary\n", 0o755),
				}
			},
			sign: true,
			want: "conflicts with prefix path",
		},
		"symlink target missing from manifest": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Files = []ArtifactFile{
					symlinkArtifactFile("bin/current", "gr-dev", 0o777),
					regularArtifactFile("docs/readme", "readme\n", 0o644),
				}
			},
			sign: true,
			want: "not declared in the manifest",
		},
		"symlink target is another symlink": {
			editArtifact: func(artifact *ArtifactContractManifest) {
				artifact.Files = []ArtifactFile{
					symlinkArtifactFile("bin/current", "gr-dev", 0o777),
					symlinkArtifactFile("bin/gr-dev", "../libexec/gr", 0o777),
					regularArtifactFile("libexec/gr", "binary\n", 0o755),
				}
			},
			sign: true,
			want: "not a regular file",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutatedArtifact := artifact.copy()
			test.editArtifact(&mutatedArtifact)

			if test.sign {
				signArtifact(t, &mutatedArtifact)
			}

			err := VerifyArtifactConsumer(manifest, plan, result, mutatedArtifact, archive, artifactConsumerOptions(plan, mutatedArtifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyArtifactConsumer() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactStrictDecodeRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()

	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
	if plan.PlanDigest == "" || result.ResultDigest == "" || len(archive) == 0 {
		t.Fatal("artifact fixture is incomplete")
	}

	data, err := artifact.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		data []byte
		want string
	}{
		"unknown field": {
			data: []byte(`{"schema_version":1,"thrawn":true}`),
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
			_, err := DecodeArtifactManifest("artifact.json", test.data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeArtifactManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactConsumerRejectsCorruptSubstitutedAndCrossCommitArtifacts(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	t.Run("corrupt archive digest", func(t *testing.T) {
		corrupt := append([]byte(nil), archive...)
		corrupt[len(corrupt)-1] ^= 0xff

		err := VerifyArtifactConsumer(manifest, plan, result, artifact, corrupt, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "archive digest mismatch") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want archive digest mismatch", err)
		}
	})

	t.Run("substituted artifact ID", func(t *testing.T) {
		err := VerifyArtifactConsumer(manifest, plan, result, artifact, archive, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-arm64"), p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "artifact ID") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want artifact ID rejection", err)
		}
	})

	t.Run("cross commit artifact", func(t *testing.T) {
		crossCommit := artifact.copy()
		crossCommit.Source.Commit = strings.Repeat("6", 40)
		signArtifact(t, &crossCommit)

		err := VerifyArtifactConsumer(manifest, plan, result, crossCommit, archive, artifactConsumerOptions(plan, crossCommit, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
		if err == nil || !strings.Contains(err.Error(), "plan identity") {
			t.Fatalf("VerifyArtifactConsumer() error = %v, want plan identity rejection", err)
		}
	})
}

func TestArtifactExtractionDoesNotWriteBeforeVerification(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, _ := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
	invalidArchive := buildArtifactArchive(t, ArtifactFormatTar, append(append([]archiveMember(nil), members...), archiveMember{
		name:     "z-extra",
		data:     "extra\n",
		mode:     0o644,
		typeflag: tar.TypeReg,
	}))
	setArtifactArchiveDigest(t, &artifact, &result, invalidArchive)

	destination := filepath.Join(t.TempDir(), "out")

	err := ExtractVerifiedArtifact(manifest, plan, result, artifact, invalidArchive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "extra member") {
		t.Fatalf("ExtractVerifiedArtifact() error = %v, want extra member rejection", err)
	}

	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination stat error = %v, want no extraction directory", statErr)
	}
}

func TestArtifactExtractionRejectsCaseFoldedPathCollisionsBeforeWriting(t *testing.T) {
	manifest := loadManifest(t)

	tests := map[string]struct {
		files   []ArtifactFile
		members []archiveMember
		want    string
	}{
		"same destination after case fold": {
			files: []ArtifactFile{
				regularArtifactFile("Bin/gr", "upper\n", 0o755),
				regularArtifactFile("bin/GR", "lower\n", 0o755),
			},
			members: []archiveMember{
				{name: "Bin/gr", data: "upper\n", mode: 0o755, typeflag: tar.TypeReg},
				{name: "bin/GR", data: "lower\n", mode: 0o755, typeflag: tar.TypeReg},
			},
			want: "collide after case folding",
		},
		"file conflicts with folded directory prefix": {
			files: []ArtifactFile{
				regularArtifactFile("Bin", "file\n", 0o644),
				regularArtifactFile("bin/gr", "binary\n", 0o755),
			},
			members: []archiveMember{
				{name: "Bin", data: "file\n", mode: 0o644, typeflag: tar.TypeReg},
				{name: "bin/gr", data: "binary\n", mode: 0o755, typeflag: tar.TypeReg},
			},
			want: "conflicts with prefix path",
		},
		"implicit directory prefixes collide after fold": {
			files: []ArtifactFile{
				regularArtifactFile("Foo/bar", "upper\n", 0o644),
				regularArtifactFile("foo/baz", "lower\n", 0o644),
			},
			members: []archiveMember{
				{name: "Foo/bar", data: "upper\n", mode: 0o644, typeflag: tar.TypeReg},
				{name: "foo/baz", data: "lower\n", mode: 0o644, typeflag: tar.TypeReg},
			},
			want: "directory prefixes",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, test.files, test.members)
			destination := filepath.Join(t.TempDir(), "out")

			err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ExtractVerifiedArtifact() error = %v, want %q", err, test.want)
			}

			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination stat error = %v, want no extraction directory", statErr)
			}
		})
	}
}

func TestArtifactExtractionRejectsExistingSymlinkParent(t *testing.T) {
	manifest := loadManifest(t)
	files := []ArtifactFile{
		regularArtifactFile("bin/gr", "binary\n", 0o755),
	}
	members := []archiveMember{
		{name: "bin/gr", data: "binary\n", mode: 0o755, typeflag: tar.TypeReg},
	}
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
	root := t.TempDir()
	destination := filepath.Join(root, "out")
	outside := filepath.Join(root, "outside")

	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, filepath.Join(destination, "bin")); err != nil {
		t.Fatal(err)
	}

	err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("ExtractVerifiedArtifact() error = %v, want non-empty destination rejection", err)
	}

	if _, statErr := os.Stat(filepath.Join(outside, "gr")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside artifact stat error = %v, want no escaped write", statErr)
	}
}

func TestArtifactPreflightRejectsExistingSymlinkParent(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")

	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, filepath.Join(root, "bin")); err != nil {
		t.Fatal(err)
	}

	err := preflightVerifiedEntries(root, []verifiedArchiveEntry{{
		file: regularArtifactFile("bin/gr", "binary\n", 0o755),
		data: []byte("binary\n"),
	}})
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("preflightVerifiedEntries() error = %v, want symlink parent rejection", err)
	}
}

func TestArtifactExtractionRejectsSymlinkDestination(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	destination := filepath.Join(root, "out")

	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, destination); err != nil {
		t.Fatal(err)
	}

	err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("ExtractVerifiedArtifact() error = %v, want symlink destination rejection", err)
	}

	if _, statErr := os.Stat(filepath.Join(outside, "gr-dev")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside artifact stat error = %v, want no escaped write", statErr)
	}
}

func TestArtifactExtractionRejectsSymlinkDestinationAncestor(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	outsideDestination := filepath.Join(outside, "out")
	link := filepath.Join(root, "link")
	destination := filepath.Join(link, "out")

	if err := os.MkdirAll(outsideDestination, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("ExtractVerifiedArtifact() error = %v, want symlink destination ancestor rejection", err)
	}

	if _, statErr := os.Stat(filepath.Join(outsideDestination, "gr-dev")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside artifact stat error = %v, want no escaped write", statErr)
	}
}

func TestArtifactExtractionRejectsSymlinkDestinationAncestorWithExistingChild(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	outsideExisting := filepath.Join(outside, "existing")
	link := filepath.Join(root, "link")
	destination := filepath.Join(link, "existing", "out")

	if err := os.MkdirAll(outsideExisting, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("ExtractVerifiedArtifact() error = %v, want symlink destination ancestor rejection", err)
	}

	if _, statErr := os.Stat(filepath.Join(outsideExisting, "out", "gr-dev")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside artifact stat error = %v, want no escaped write", statErr)
	}
}

func TestArtifactExtractionAllowsSystemRootSymlinkAncestor(t *testing.T) {
	symlinkRoot := systemRootSymlinkDir(t)

	manifest := loadManifest(t)
	files := []ArtifactFile{
		regularArtifactFile("bin/gr", "binary\n", 0o755),
	}
	members := []archiveMember{
		{name: "bin/gr", data: "binary\n", mode: 0o755, typeflag: tar.TypeReg},
	}
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)

	root, err := os.MkdirTemp(symlinkRoot, "graith-artifact-")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(root) })

	destination := filepath.Join(root, "out")

	if err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow); err != nil {
		t.Fatalf("ExtractVerifiedArtifact() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(destination, "bin", "gr"))
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "binary\n" {
		t.Fatalf("extracted binary = %q, want binary content", data)
	}
}

func TestArtifactExtractionPreflightsDestinationBeforeWriting(t *testing.T) {
	manifest := loadManifest(t)
	files := releaseArtifactFiles()
	members := releaseArchiveMembers()
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
	destination := filepath.Join(t.TempDir(), "out")

	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(destination, "README.md"), []byte("ambient\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("ExtractVerifiedArtifact() error = %v, want non-empty destination rejection", err)
	}

	if _, statErr := os.Stat(filepath.Join(destination, "LICENSE")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("LICENSE stat error = %v, want no partial write before conflict", statErr)
	}

	data, err := os.ReadFile(filepath.Join(destination, "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "ambient\n" {
		t.Fatalf("README.md = %q, want existing content preserved", data)
	}
}

func TestArtifactExtractionRejectsAmbientDestinationMembers(t *testing.T) {
	manifest := loadManifest(t)
	files := []ArtifactFile{
		regularArtifactFile("bin/gr", "binary\n", 0o755),
	}
	members := []archiveMember{
		{name: "bin/gr", data: "binary\n", mode: 0o755, typeflag: tar.TypeReg},
	}
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
	destination := filepath.Join(t.TempDir(), "out")

	if err := os.MkdirAll(filepath.Join(destination, "stowaway-dir"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(destination, "stowaway.txt"), []byte("ambient\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("ExtractVerifiedArtifact() error = %v, want non-empty destination rejection", err)
	}

	if _, statErr := os.Stat(filepath.Join(destination, "bin")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bin stat error = %v, want no extraction into non-empty destination", statErr)
	}
}

func TestArtifactExtractionPreflightDoesNotCreateParentsBeforeLaterConflict(t *testing.T) {
	manifest := loadManifest(t)
	files := []ArtifactFile{
		regularArtifactFile("a/gr", "binary\n", 0o755),
		regularArtifactFile("z-readme.md", "readme\n", 0o644),
	}
	members := []archiveMember{
		{name: "a/gr", data: "binary\n", mode: 0o755, typeflag: tar.TypeReg},
		{name: "z-readme.md", data: "readme\n", mode: 0o644, typeflag: tar.TypeReg},
	}
	plan, result, artifact, archive := artifactFixture(t, manifest, ArtifactTypeRelease, "graith-dev-linux-amd64", ArtifactFormatTar, testProducerWorkflow, files, members)
	destination := filepath.Join(t.TempDir(), "out")

	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(destination, "z-readme.md"), []byte("ambient\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ExtractVerifiedArtifact(manifest, plan, result, artifact, archive, destination, artifactConsumerOptions(plan, artifact, ArtifactTypeRelease, "graith-dev-linux-amd64"), p2TestNow)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("ExtractVerifiedArtifact() error = %v, want non-empty destination rejection", err)
	}

	if _, statErr := os.Stat(filepath.Join(destination, "a")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a directory stat error = %v, want no parent directory created before conflict", statErr)
	}
}

func artifactFixture(
	t *testing.T,
	manifest Manifest,
	artifactType string,
	artifactID string,
	format string,
	workflow string,
	files []ArtifactFile,
	members []archiveMember,
) (RunPlan, ArtifactProducerResult, ArtifactContractManifest, []byte) {
	t.Helper()

	archive := buildArtifactArchive(t, format, members)
	archiveDigest := sha256Hex(archive)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"libghostty-native.lock.json"}, nil, true)
	job := planJobByMode(t, plan, "legacy/libghostty-native/native-gate")
	attempt := resultAttempt(1, "success", "", p2TestNow)
	attempt.ArtifactDigest = archiveDigest

	result, err := NewArtifactProducerResult(plan, job, []ArtifactProducerAttempt{attempt})
	if err != nil {
		t.Fatal(err)
	}

	artifact, err := NewArtifactManifest(manifest, plan, result, ArtifactManifestInput{
		ArtifactType:   artifactType,
		ArtifactID:     artifactID,
		ArtifactFormat: format,
		ArtifactDigest: archiveDigest,
		Dependencies:   artifactDependencies(),
		Toolchains:     artifactToolchains(),
		BuildFlags:     artifactBuildFlags(),
		Files:          files,
		Provenance: ArtifactProvenance{
			Workflow:       workflow,
			WorkflowSHA256: testProducerWorkflowSHA256,
			RunID:          testProducerRunID,
			RunAttempt:     1,
			JobID:          "native-gate",
			JobName:        "Native backend gate",
			ProducerStatus: "success",
			UploadComplete: true,
			ArtifactID:     artifactID,
			ArtifactDigest: archiveDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return plan, result, artifact, archive
}

func artifactConsumerOptions(plan RunPlan, artifact ArtifactContractManifest, artifactType, artifactID string) ArtifactVerificationOptions {
	return ArtifactVerificationOptions{
		ArtifactType:       artifactType,
		ArtifactID:         artifactID,
		ArtifactDigest:     artifact.ArtifactDigest,
		ProducerMode:       artifact.Mode,
		ProducerCoordinate: artifact.Coordinate,
		ConsumerPlan:       plan,
		ConsumerJob:        consumerJobForArtifact(plan, artifact),
		Workflow:           artifact.Provenance.Workflow,
		RunID:              artifact.Provenance.RunID,
		RunAttempt:         artifact.Provenance.RunAttempt,
	}
}

func consumerJobForArtifact(plan RunPlan, artifact ArtifactContractManifest) PlanJob {
	for _, job := range plan.Jobs {
		if job.Mode == artifact.Mode && job.Coordinate == artifact.Coordinate {
			return job
		}
	}

	return PlanJob{}
}

func assertExtractedArtifactFiles(t *testing.T, root string, files []ArtifactFile) {
	t.Helper()

	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))

		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}

		switch file.Kind {
		case ArtifactFileRegular:
			if !info.Mode().IsRegular() {
				t.Fatalf("%s mode = %s, want regular file", file.Path, info.Mode())
			}

			if got := int64(info.Mode().Perm()); got != file.Mode {
				t.Fatalf("%s mode = %#o, want %#o", file.Path, got, file.Mode)
			}
		case ArtifactFileSymlink:
			if info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("%s mode = %s, want symlink", file.Path, info.Mode())
			}

			target, err := os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}

			if target != file.LinkTarget {
				t.Fatalf("%s target = %q, want %q", file.Path, target, file.LinkTarget)
			}
		default:
			t.Fatalf("%s has unexpected kind %q", file.Path, file.Kind)
		}
	}
}

func releaseArtifactFiles() []ArtifactFile {
	return []ArtifactFile{
		regularArtifactFile("LICENSE", "license\n", 0o644),
		regularArtifactFile("README.md", "readme\n", 0o644),
		symlinkArtifactFile("current", "gr-dev", 0o777),
		regularArtifactFile("gr-dev", "release-binary\n", 0o755),
	}
}

func releaseArchiveMembers() []archiveMember {
	return []archiveMember{
		{name: "LICENSE", data: "license\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "README.md", data: "readme\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "current", linkTarget: "gr-dev", mode: 0o777, typeflag: tar.TypeSymlink},
		{name: "gr-dev", data: "release-binary\n", mode: 0o755, typeflag: tar.TypeReg},
	}
}

func regularArtifactFile(name, data string, mode int64) ArtifactFile {
	return ArtifactFile{
		Path:   name,
		Kind:   ArtifactFileRegular,
		Mode:   mode,
		Size:   int64(len(data)),
		SHA256: sha256Hex([]byte(data)),
	}
}

func symlinkArtifactFile(name, target string, mode int64) ArtifactFile {
	return ArtifactFile{
		Path:       name,
		Kind:       ArtifactFileSymlink,
		Mode:       mode,
		SHA256:     sha256Hex([]byte(target)),
		LinkTarget: target,
	}
}

func artifactDependencies() []IdentityDigest {
	return []IdentityDigest{
		{ID: "ghostty", Version: "15484b6", Digest: strings.Repeat("d", 64)},
		{ID: "go-libghostty", Version: "v0.0.0-20260724", Digest: strings.Repeat("e", 64)},
	}
}

func artifactToolchains() []IdentityDigest {
	return []IdentityDigest{
		{ID: "go", Version: "1.26.5", Digest: strings.Repeat("a", 64)},
		{ID: "zig", Version: "0.15.1", Digest: strings.Repeat("b", 64)},
	}
}

func artifactBuildFlags() []BuildFlag {
	return []BuildFlag{
		{Name: "cgo_enabled", Value: "1"},
		{Name: "tags", Value: "libghostty"},
	}
}

func resultAttempt(attempt int, status, failureClass string, started time.Time) ArtifactProducerAttempt {
	return ArtifactProducerAttempt{
		Attempt:        attempt,
		Status:         status,
		FailureClass:   failureClass,
		StartedAt:      started,
		CompletedAt:    started.Add(time.Minute),
		EvidenceDigest: strings.Repeat("a", 64),
		ArtifactDigest: strings.Repeat("b", 64),
	}
}

func setFinalOutcome(t *testing.T, result *ArtifactProducerResult, status, failureClass, supersededBy string) {
	t.Helper()

	final := result.Attempts[len(result.Attempts)-1]
	final.Status = status
	final.FailureClass = failureClass

	result.Attempts[len(result.Attempts)-1] = final
	if len(result.Attempts) == 1 {
		result.FirstStatus = status
		result.FirstFailureClass = failureClass
	}

	result.Status = status
	result.FailureClass = failureClass
	result.SupersededBy = supersededBy
	signResult(t, result)
}

func signResult(t *testing.T, result *ArtifactProducerResult) {
	t.Helper()

	digest, err := result.Digest()
	if err != nil {
		t.Fatal(err)
	}

	result.ResultDigest = digest
}

func planJobByMode(t *testing.T, plan RunPlan, mode string) PlanJob {
	t.Helper()

	for _, job := range plan.Jobs {
		if job.Mode == mode {
			return job
		}
	}

	t.Fatalf("missing plan job for mode %s", mode)

	return PlanJob{}
}

func buildArtifactArchive(t *testing.T, format string, members []archiveMember) []byte {
	t.Helper()

	var output bytes.Buffer

	var writer io.WriteCloser = nopWriteCloser{Writer: &output}

	switch format {
	case ArtifactFormatTar:
	case ArtifactFormatTarGzip:
		gzipWriter := gzip.NewWriter(&output)
		gzipWriter.Name = ""
		gzipWriter.ModTime = p2TestNow
		writer = gzipWriter
	default:
		t.Fatalf("unsupported test artifact format %s", format)
	}

	tarWriter := tar.NewWriter(writer)

	for _, member := range members {
		header := &tar.Header{
			Name:       member.name,
			Mode:       member.mode,
			Typeflag:   member.typeflag,
			Format:     member.format,
			AccessTime: member.accessTime,
			PAXRecords: member.paxRecords,
			Xattrs:     member.xattrs,
		}

		switch member.typeflag {
		case tar.TypeReg:
			header.Size = int64(len(member.data))
		case tar.TypeSymlink, tar.TypeLink:
			header.Linkname = member.linkTarget
		case tar.TypeDir:
		default:
			t.Fatalf("unsupported test tar type %q", member.typeflag)
		}

		if member.forceSize != 0 {
			header.Size = member.forceSize
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}

		if header.Size > 0 {
			if _, err := tarWriter.Write([]byte(member.data)); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return output.Bytes()
}

func systemRootSymlinkDir(t *testing.T) string {
	t.Helper()

	for _, candidate := range []string{"/tmp", "/var"} {
		info, err := os.Lstat(candidate)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}

		targetInfo, err := os.Stat(candidate)
		if err != nil || !targetInfo.IsDir() {
			continue
		}

		return candidate
	}

	t.Skip("no system root symlink directory available")

	return ""
}

func gzipPayload(t *testing.T, payload []byte) []byte {
	t.Helper()

	var output bytes.Buffer

	writer := gzip.NewWriter(&output)
	writer.Name = ""
	writer.ModTime = p2TestNow

	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return output.Bytes()
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error {
	return nil
}

func setArtifactArchiveDigest(t *testing.T, artifact *ArtifactContractManifest, result *ArtifactProducerResult, archive []byte) {
	t.Helper()

	digest := sha256Hex(archive)
	artifact.ArtifactDigest = digest
	artifact.Provenance.ArtifactDigest = digest
	result.Attempts[len(result.Attempts)-1].ArtifactDigest = digest
	result.ArtifactDigest = digest
	signResult(t, result)
	artifact.ResultDigest = result.ResultDigest
	signArtifact(t, artifact)
}

func setArtifactResultCompletedAt(t *testing.T, artifact *ArtifactContractManifest, result *ArtifactProducerResult, completedAt time.Time) {
	t.Helper()

	setResultCompletedAt(t, result, completedAt)
	artifact.ResultDigest = result.ResultDigest
	signArtifact(t, artifact)
}

func setResultCompletedAt(t *testing.T, result *ArtifactProducerResult, completedAt time.Time) {
	t.Helper()

	final := result.Attempts[len(result.Attempts)-1]
	final.CompletedAt = completedAt
	result.Attempts[len(result.Attempts)-1] = final
	result.CompletedAt = completedAt
	signResult(t, result)
}

func signArtifact(t *testing.T, artifact *ArtifactContractManifest) {
	t.Helper()

	digest, err := artifact.Digest()
	if err != nil {
		t.Fatal(err)
	}

	artifact.ManifestDigest = digest
}

func buildTestPlanAt(t *testing.T, manifest Manifest, event EventInput, createdAt, expiresAt, now time.Time) RunPlan {
	t.Helper()

	plan, err := BuildPlan(manifest, PlanOptions{
		Event:         event,
		ChangedFiles:  []string{"libghostty-native.lock.json"},
		ExactFileList: true,
		CreatedAt:     createdAt,
		ExpiresAt:     expiresAt,
		Now:           now,
	})
	if err != nil {
		t.Fatal(err)
	}

	return plan
}
