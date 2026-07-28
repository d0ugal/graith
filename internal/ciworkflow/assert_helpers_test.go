package ciworkflow

import (
	"slices"
	"testing"
)

func assertStringsEqual(t *testing.T, label string, got, want []string) {
	t.Helper()

	got = sortedStrings(got)
	want = sortedStrings(want)

	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want exactly %v", label, got, want)
	}
}

func regenerationPushOperation(trustTier string) CredentialOperation {
	return CredentialOperation{
		Operation:  "regeneration-push",
		TrustTier:  trustTier,
		Capability: "generated-metadata",
		Token: SyntheticToken{
			Name:         "release",
			TrustTier:    trustTier,
			Class:        syntheticMaintainerToken,
			Scopes:       []string{"contents:write"},
			AllowedRoots: []string{"generated"},
		},
		Target: "generated/braw.bundle",
	}
}

func devReleasePublishOperation(trustTier string) CredentialOperation {
	return CredentialOperation{
		Operation:  "dev-release-publish",
		TrustTier:  trustTier,
		Capability: "dev-release",
		Token: SyntheticToken{
			Name:         "release",
			TrustTier:    trustTier,
			Class:        syntheticMaintainerToken,
			Scopes:       []string{"contents:write"},
			AllowedRoots: []string{"dist/dev-release"},
		},
		Target: "dist/dev-release/graith-dev.tar.gz",
	}
}
