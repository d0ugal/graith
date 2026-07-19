package daemonservice

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOSUserHomeRequiresEffectiveOwnedAccountHome(t *testing.T) {
	original := lookupOSUserByID

	t.Cleanup(func() { lookupOSUserByID = original })

	home := t.TempDir()
	lookupOSUserByID = func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Geteuid()), HomeDir: home, Username: "canny"}, nil
	}

	got, err := OSUserHome(os.Geteuid())
	if err != nil || got != home {
		t.Fatalf("OSUserHome() = %q, %v, want %q", got, err, home)
	}

	if _, err := OSUserHome(os.Geteuid() + 1); err == nil {
		t.Fatal("OSUserHome accepted a UID other than the effective user")
	}

	lookupOSUserByID = func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Geteuid()), HomeDir: "/", Username: "canny"}, nil
	}

	if _, err := OSUserHome(os.Geteuid()); err == nil {
		t.Fatal("OSUserHome accepted the filesystem root as an account home")
	}
}

const (
	testVersion     = "1.2.3"
	testCommit      = "abc123"
	testTeam        = "CANNYTEAM"
	testRequirement = "identifier net.graith.service and anchor apple generic"
)

func writeBundleFixture(t *testing.T, root string) (string, string) {
	return writeBundleFixtureFor(t, root, testVersion, testCommit, []byte("braw daemon payload"))
}

func writeBundleFixtureFor(t *testing.T, root, version, commit string, payload []byte) (string, string) {
	t.Helper()

	app := filepath.Join(root, AppBundleName)

	macOS := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil { // #nosec G301 -- executable bundle fixture.
		t.Fatal(err)
	}

	embedded := filepath.Join(macOS, DaemonExecutable)
	if err := os.WriteFile(embedded, payload, 0o755); err != nil { // #nosec G306 -- executable test fixture.
		t.Fatal(err)
	}

	standalone := filepath.Join(root, "gr")
	if err := os.WriteFile(standalone, payload, 0o755); err != nil { // #nosec G306 -- executable test fixture.
		t.Fatal(err)
	}

	hash, err := hashRegularFile(embedded)
	if err != nil {
		t.Fatal(err)
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>%s</string>
<key>CFBundleShortVersionString</key><string>%s</string>
<key>CFBundleVersion</key><string>%s</string>
<key>GraithCommitSHA</key><string>%s</string>
<key>GraithPayloadSHA256</key><string>%s</string>
</dict></plist>`, ServiceManifest().BundleIdentifier, version, commit, commit, hash)
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil { // #nosec G306 -- public plist fixture.
		t.Fatal(err)
	}

	return app, standalone
}

func bundleExpectations(standalone string) BundleExpectations {
	return BundleExpectations{
		Version: testVersion, Commit: testCommit, StandalonePath: standalone,
		TeamID: testTeam, Requirement: testRequirement,
		VerifySignature: func(string) (SignatureInfo, error) {
			return SignatureInfo{Identifier: ServiceManifest().BundleIdentifier, TeamID: testTeam, Requirement: testRequirement}, nil
		},
	}
}

func TestValidateBundleAndTamperFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	app, standalone := writeBundleFixture(t, root)
	expectations := bundleExpectations(standalone)

	validated, err := ValidateBundle(app, expectations)
	if err != nil {
		t.Fatalf("ValidateBundle() = %v", err)
	}

	if validated.Generation.ID != testVersion+"-"+testCommit || validated.Generation.PayloadHash == "" {
		t.Fatalf("validated generation = %#v", validated.Generation)
	}

	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", DaemonExecutable), []byte("thrawn"), 0o755); err != nil { // #nosec G306 -- executable negative fixture.
		t.Fatal(err)
	}

	if _, err := ValidateBundle(app, expectations); err == nil || !strings.Contains(err.Error(), "payload hash") {
		t.Fatalf("tampered ValidateBundle() = %v", err)
	}
}

func TestManagedInstallationValidationIsReadOnlyAndExact(t *testing.T) {
	root := t.TempDir()
	app, standalone := writeBundleFixture(t, root)
	expectations := bundleExpectations(standalone)
	verified := ""
	expectations.VerifyDistribution = func(path string) error {
		verified = path
		return nil
	}

	if err := validateManagedInstallation(standalone, expectations); err != nil {
		t.Fatal(err)
	}

	if verified != app {
		t.Fatalf("distribution verified %q, want %q", verified, app)
	}

	if err := validateManagedInstallation(filepath.Join(root, "missing", "gr"), expectations); err == nil {
		t.Fatal("missing package-associated app was accepted")
	}
}

func TestManagedBuildSigningExpectationValidation(t *testing.T) {
	originalManaged := ManagedBuild
	originalTeam := ExpectedTeamID
	originalRequirement := ExpectedRequirementBase64

	t.Cleanup(func() {
		ManagedBuild = originalManaged
		ExpectedTeamID = originalTeam
		ExpectedRequirementBase64 = originalRequirement
	})

	ManagedBuild = "true"

	if !IsManagedBuild() {
		t.Fatal("managed build marker was ignored")
	}

	ExpectedTeamID = "CANNYTEAM"
	ExpectedRequirementBase64 = base64.StdEncoding.EncodeToString([]byte("identifier net.graith.service"))

	team, requirement, err := stableSigningExpectation()
	if err != nil || team != ExpectedTeamID || requirement != "identifier net.graith.service" {
		t.Fatalf("stableSigningExpectation() = (%q, %q, %v)", team, requirement, err)
	}

	ExpectedRequirementBase64 = ""

	if _, _, err := stableSigningExpectation(); err == nil {
		t.Fatal("incomplete stable signing expectation was accepted")
	}
}

func TestValidateBundleRejectsSigningAndParityMismatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	app, standalone := writeBundleFixture(t, root)
	expectations := bundleExpectations(standalone)

	expectations.TeamID = "WRONGTEAM"
	if _, err := ValidateBundle(app, expectations); err == nil || !strings.Contains(err.Error(), "signing team") {
		t.Fatalf("wrong-team ValidateBundle() = %v", err)
	}

	expectations = bundleExpectations(standalone)
	if err := os.WriteFile(standalone, []byte("different"), 0o755); err != nil { // #nosec G306 -- executable mismatch fixture.
		t.Fatal(err)
	}

	if _, err := ValidateBundle(app, expectations); err == nil || !strings.Contains(err.Error(), "payloads differ") {
		t.Fatalf("parity ValidateBundle() = %v", err)
	}
}

func TestValidateBundleRejectsSymlinkAndInsecureAncestor(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	realRoot := filepath.Join(root, "real")
	if err := os.Mkdir(realRoot, 0o755); err != nil { // #nosec G301 -- traversable app fixture.
		t.Fatal(err)
	}

	app, standalone := writeBundleFixture(t, realRoot)

	link := filepath.Join(root, AppBundleName)
	if err := os.Symlink(app, link); err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateBundle(link, bundleExpectations(standalone)); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink ValidateBundle() = %v", err)
	}

	if err := os.Chmod(realRoot, 0o777); err != nil { // #nosec G302 -- intentionally insecure negative fixture.
		t.Fatal(err)
	}

	if _, err := ValidateBundle(app, bundleExpectations(standalone)); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("insecure-ancestor ValidateBundle() = %v", err)
	}
}

func TestBundleCandidatesTarballHomebrewAndEmbedded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		executable string
		want       string
	}{
		{executable: "/bothy/gr", want: "/bothy/Graith.app"},
		{executable: "/opt/homebrew/Cellar/graith/1.2.3/bin/gr", want: "/opt/homebrew/Cellar/graith/1.2.3/libexec/graith/Graith.app"},
		{executable: "/bothy/Graith.app/Contents/MacOS/gr", want: "/bothy/Graith.app"},
	}
	for _, tt := range tests {
		found := false

		for _, candidate := range BundleCandidates(tt.executable) {
			if candidate == tt.want {
				found = true
			}
		}

		if !found {
			t.Errorf("BundleCandidates(%q) = %v, missing %q", tt.executable, BundleCandidates(tt.executable), tt.want)
		}
	}
}

func TestDiscoverBundleMissingVsPresentInvalid(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	executable := filepath.Join(root, "gr")
	if err := os.WriteFile(executable, []byte("braw"), 0o755); err != nil { // #nosec G306 -- executable discovery fixture.
		t.Fatal(err)
	}

	if _, present, err := DiscoverBundle(executable, bundleExpectations(executable)); err != nil || present {
		t.Fatalf("missing DiscoverBundle() = present %v, err %v", present, err)
	}

	if err := os.Mkdir(filepath.Join(root, AppBundleName), 0o755); err != nil { // #nosec G301 -- traversable malformed app fixture.
		t.Fatal(err)
	}

	if _, present, err := DiscoverBundle(executable, bundleExpectations(executable)); err == nil || !present {
		t.Fatalf("invalid DiscoverBundle() = present %v, err %v", present, err)
	}
}

func TestCacheBundleIsImmutableAndRevalidated(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	app, standalone := writeBundleFixture(t, sourceRoot)
	expectations := bundleExpectations(standalone)

	source, err := ValidateBundle(app, expectations)
	if err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(t.TempDir(), "services")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}

	first, err := cacheBundleAtRoot(source, expectations, cache)
	if err != nil {
		t.Fatal(err)
	}

	second, err := cacheBundleAtRoot(source, expectations, cache)
	if err != nil {
		t.Fatal(err)
	}

	if first.AppPath != second.AppPath || first.AppPath == source.AppPath {
		t.Fatalf("cache paths first=%q second=%q source=%q", first.AppPath, second.AppPath, source.AppPath)
	}

	if err := os.WriteFile(filepath.Join(first.AppPath, "Contents", "MacOS", DaemonExecutable), []byte("tampered cache"), 0o755); err != nil { // #nosec G306 -- intentionally tampered executable fixture.
		t.Fatal(err)
	}

	if _, err := cacheBundleAtRoot(source, expectations, cache); err == nil {
		t.Fatal("tampered existing cache accepted")
	}
}

func TestCacheBundleHonorsAggregateStartupCancellation(t *testing.T) {
	sourceRoot := t.TempDir()
	app, standalone := writeBundleFixture(t, sourceRoot)
	expectations := bundleExpectations(standalone)

	source, err := ValidateBundle(app, expectations)
	if err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(t.TempDir(), "services")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cacheBundleAtRootContext(ctx, source, expectations, cache); !errors.Is(err, context.Canceled) {
		t.Fatalf("cacheBundleAtRootContext() = %v, want canceled startup", err)
	}

	if _, err := os.Stat(filepath.Join(cache, source.Generation.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled cache install created a generation: %v", err)
	}
}

func TestCacheBundleVerifiesDistributionBeforePublishingGeneration(t *testing.T) {
	sourceRoot := t.TempDir()
	app, standalone := writeBundleFixture(t, sourceRoot)
	expectations := bundleExpectations(standalone)

	source, err := ValidateBundle(app, expectations)
	if err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(t.TempDir(), "services")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}

	called := false

	expectations.VerifyDistribution = func(path string) error {
		called = true

		if filepath.Base(path) != AppBundleName {
			t.Fatalf("distribution verifier path = %q", path)
		}

		return errors.New("dreich staple")
	}
	if _, err := cacheBundleAtRoot(source, expectations, cache); err == nil || !strings.Contains(err.Error(), "dreich staple") {
		t.Fatalf("cacheBundleAtRoot() distribution error = %v", err)
	}

	if !called {
		t.Fatal("cache copy skipped distribution verification")
	}

	if _, err := os.Stat(filepath.Join(cache, source.Generation.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed distribution was published in cache: %v", err)
	}
}

func TestValidatedCachedBundlesIgnoresUnpublishedPrivateEntries(t *testing.T) {
	sourceRoot := t.TempDir()
	app, standalone := writeBundleFixture(t, sourceRoot)
	expectations := bundleExpectations(standalone)

	source, err := ValidateBundle(app, expectations)
	if err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(t.TempDir(), "services")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := cacheBundleAtRoot(source, expectations, cache); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cache, ".DS_Store"), []byte("dreich"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(cache, ".install-bothy"), 0o700); err != nil {
		t.Fatal(err)
	}

	bundles, err := validatedCachedBundles(cache, os.Getuid(), expectations)
	if err != nil {
		t.Fatal(err)
	}

	if len(bundles) != 1 || bundles[0].Generation.ID != source.Generation.ID {
		t.Fatalf("validated bundles = %#v, want only %s", bundles, source.Generation.ID)
	}

	if err := os.WriteFile(filepath.Join(cache, "thrawn"), []byte("unsafe visible entry"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := validatedCachedBundles(cache, os.Getuid(), expectations); err == nil {
		t.Fatal("visible malformed cache generation was ignored")
	}
}
