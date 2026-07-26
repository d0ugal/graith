package cigate

import (
	"strings"
	"testing"
	"time"
)

func TestValidateLiveProofBundleRejectsLocalEmulation(t *testing.T) {
	bundle := validLiveProofBundle()
	bundle.Source = "hermetic"

	err := ValidateLiveProofBundle(bundle, validConfig(), time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "cannot satisfy GitHub fixture claims") {
		t.Fatalf("ValidateLiveProofBundle() error = %v, want local-source rejection", err)
	}
}

func TestValidateLiveProofBundleRequiresEveryCase(t *testing.T) {
	bundle := validLiveProofBundle()
	bundle.Cases = bundle.Cases[:len(bundle.Cases)-1]

	err := ValidateLiveProofBundle(bundle, validConfig(), time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "missing required case") {
		t.Fatalf("ValidateLiveProofBundle() error = %v, want missing-case rejection", err)
	}
}

func TestValidateLiveProofBundleRejectsInvalidFixtureControls(t *testing.T) {
	now := time.Now().UTC()
	tests := map[string]struct {
		mutate func(*LiveProofBundle)
		want   string
	}{
		"app id mismatch": {
			mutate: func(bundle *LiveProofBundle) { bundle.RulesetCheckAppID++ },
			want:   "installed App id",
		},
		"fixture repository placeholder": {
			mutate: func(bundle *LiveProofBundle) { bundle.FixtureRepository = "tbd-fixture" },
			want:   "fixture repository",
		},
		"fixture repository is protected repository": {
			mutate: func(bundle *LiveProofBundle) { bundle.FixtureRepository = "d0ugal/graith" },
			want:   "fixture repository must be separate",
		},
		"placeholder event delivery": {
			mutate: func(bundle *LiveProofBundle) { bundle.Cases[0].EventDeliveryID = "pending-delivery" },
			want:   "event delivery id",
		},
		"placeholder check id": {
			mutate: func(bundle *LiveProofBundle) { bundle.Cases[0].RequiredCheckID = "todo-check" },
			want:   "retained check/evidence identity",
		},
		"placeholder evidence uri": {
			mutate: func(bundle *LiveProofBundle) { bundle.Cases[0].EvidenceURI = "file:///tmp/case.json" },
			want:   "retained check/evidence identity",
		},
		"bypass actors": {
			mutate: func(bundle *LiveProofBundle) { bundle.NoBypassActors = false },
			want:   "no bypass actors",
		},
		"merge queue disabled": {
			mutate: func(bundle *LiveProofBundle) { bundle.MergeQueueEnabled = false },
			want:   "merge queue enabled",
		},
		"future collection": {
			mutate: func(bundle *LiveProofBundle) { bundle.CollectedAt = now.Add(10 * time.Minute) },
			want:   "collection time",
		},
		"stale collection": {
			mutate: func(bundle *LiveProofBundle) { bundle.CollectedAt = now.Add(-31 * 24 * time.Hour) },
			want:   "older than the 30-day",
		},
		"duplicate case id": {
			mutate: func(bundle *LiveProofBundle) { bundle.Cases[1].ID = bundle.Cases[0].ID },
			want:   "duplicate live proof case",
		},
		"non-passed case": {
			mutate: func(bundle *LiveProofBundle) { bundle.Cases[0].Status = "failed" },
			want:   "want passed",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			bundle := validLiveProofBundle()
			test.mutate(&bundle)

			err := ValidateLiveProofBundle(bundle, validConfig(), now)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateLiveProofBundle() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateLiveProofBundleRejectsReusedCaseEvidenceIdentity(t *testing.T) {
	tests := map[string]struct {
		mutate func([]LiveProofCase)
		want   string
	}{
		"event delivery": {
			mutate: func(cases []LiveProofCase) { cases[1].EventDeliveryID = cases[0].EventDeliveryID },
			want:   "event delivery",
		},
		"artifact digest": {
			mutate: func(cases []LiveProofCase) { cases[1].ArtifactDigest = cases[0].ArtifactDigest },
			want:   "artifact digest",
		},
		"required check id": {
			mutate: func(cases []LiveProofCase) { cases[1].RequiredCheckID = cases[0].RequiredCheckID },
			want:   "required check id",
		},
		"evidence uri": {
			mutate: func(cases []LiveProofCase) { cases[1].EvidenceURI = cases[0].EvidenceURI },
			want:   "evidence URI",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			bundle := validLiveProofBundle()
			test.mutate(bundle.Cases)

			err := ValidateLiveProofBundle(bundle, validConfig(), time.Now().UTC())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateLiveProofBundle() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateLiveProofBundleRejectsPlaceholderExternalDecisions(t *testing.T) {
	tests := map[string]struct {
		mutate func(*LiveProofBundle)
		want   string
	}{
		"localhost runtime": {
			mutate: func(bundle *LiveProofBundle) { bundle.ExternalDecisions.Runtime = "http://localhost:8080" },
			want:   "runtime",
		},
		"pending key service": {
			mutate: func(bundle *LiveProofBundle) { bundle.ExternalDecisions.AttestationKeyService = "pending-kms" },
			want:   "attestation key service",
		},
		"pending key id": {
			mutate: func(bundle *LiveProofBundle) { bundle.ExternalDecisions.AttestationKeyID = "pending-key" },
			want:   "attestation key id",
		},
		"local operator": {
			mutate: func(bundle *LiveProofBundle) { bundle.ExternalDecisions.OperatorOwner = "local-operator" },
			want:   "operator owner",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			bundle := validLiveProofBundle()
			test.mutate(&bundle)

			err := ValidateLiveProofBundle(bundle, validConfig(), time.Now().UTC())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateLiveProofBundle() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateLiveProofBundleRejectsTrustedConfigMismatch(t *testing.T) {
	tests := map[string]struct {
		mutate func(*LiveProofBundle, *Config)
		want   string
	}{
		"app id": {
			mutate: func(_ *LiveProofBundle, config *Config) { config.App.ID++ },
			want:   "trusted config App id",
		},
		"app slug": {
			mutate: func(bundle *LiveProofBundle, _ *Config) { bundle.AppSlug = "canny-ci-gate" },
			want:   "app slug",
		},
		"fixture repository": {
			mutate: func(bundle *LiveProofBundle, _ *Config) {
				bundle.FixtureRepository = "d0ugal/other-fixture"
			},
			want: "fixture repository",
		},
		"runtime": {
			mutate: func(bundle *LiveProofBundle, _ *Config) {
				bundle.ExternalDecisions.Runtime = "fixture-hosted-runtime-v2"
			},
			want: "runtime",
		},
		"attestation key service": {
			mutate: func(bundle *LiveProofBundle, _ *Config) {
				bundle.ExternalDecisions.AttestationKeyService = "fixture-attestation-kms-v2"
			},
			want: "attestation key service",
		},
		"attestation key id": {
			mutate: func(bundle *LiveProofBundle, _ *Config) {
				bundle.ExternalDecisions.AttestationKeyID = "projects/braw/locations/global/keyRings/canny/cryptoKeys/backup"
			},
			want: "attestation key id",
		},
		"trust model": {
			mutate: func(bundle *LiveProofBundle, _ *Config) {
				bundle.ExternalDecisions.TrustModel = "reviewed-release-digest-signed-by-backup-key"
			},
			want: "trust model",
		},
		"rotation owner": {
			mutate: func(bundle *LiveProofBundle, _ *Config) {
				bundle.ExternalDecisions.RotationOwner = "graith-release-owners"
			},
			want: "rotation owner",
		},
		"retention owner": {
			mutate: func(bundle *LiveProofBundle, _ *Config) {
				bundle.ExternalDecisions.RetentionOwner = "graith-release-owners"
			},
			want: "retention owner",
		},
		"revocation owner": {
			mutate: func(bundle *LiveProofBundle, _ *Config) {
				bundle.ExternalDecisions.RevocationOwner = "graith-release-owners"
			},
			want: "revocation owner",
		},
		"operator owner": {
			mutate: func(bundle *LiveProofBundle, _ *Config) {
				bundle.ExternalDecisions.OperatorOwner = "graith-release-owners"
			},
			want: "operator owner",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			bundle := validLiveProofBundle()
			config := validConfig()
			test.mutate(&bundle, &config)

			err := ValidateLiveProofBundle(bundle, config, time.Now().UTC())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateLiveProofBundle() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateLiveProofBundleAcceptsCompleteLiveFixtureShape(t *testing.T) {
	bundle := validLiveProofBundle()

	if err := ValidateLiveProofBundle(bundle, validConfig(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func validLiveProofBundle() LiveProofBundle {
	now := time.Now().UTC().Add(-time.Hour)
	cases := make([]LiveProofCase, 0, len(requiredLiveProofCases))

	for _, id := range requiredLiveProofCases {
		cases = append(cases, LiveProofCase{
			ID:              id,
			Status:          "passed",
			EventDeliveryID: "delivery-" + id,
			HeadSHA:         strings.Repeat("1", 40),
			BaseSHA:         strings.Repeat("2", 40),
			ArtifactDigest:  digestHex("artifact", id),
			RequiredCheckID: "check-" + id,
			EvidenceURI:     "artifact-store://braw/live/" + id,
		})
	}

	return LiveProofBundle{
		SchemaVersion:     SchemaVersion,
		Source:            LiveProofSource,
		FixtureRepository: "d0ugal/graith-ci-gate-fixture",
		AppID:             424242,
		AppSlug:           CheckName,
		RulesetCheckAppID: 424242,
		NoBypassActors:    true,
		MergeQueueEnabled: true,
		CollectedAt:       now,
		Cases:             cases,
		ExternalDecisions: ExternalDecisions{
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
