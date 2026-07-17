package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  []int
	}{
		{"v1.2.3", []int{1, 2, 3}},
		{"1.2.3", []int{1, 2, 3}},
		{"v0.2.1", []int{0, 2, 1}},
		{"0.10.0", []int{0, 10, 0}},
		{"v1.0.0-rc1", []int{1, 0, 0}},
		{"dev", nil},
		{"", nil},
		{"1.2", nil},
		{"abc", nil},
	}

	for _, tt := range tests {
		got := parseVersion(tt.input)
		if tt.want == nil {
			if got != nil {
				t.Errorf("parseVersion(%q) = %v, want nil", tt.input, got)
			}

			continue
		}

		if got == nil {
			t.Errorf("parseVersion(%q) = nil, want %v", tt.input, tt.want)
			continue
		}

		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Errorf("parseVersion(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest  string
		current string
		want    bool
	}{
		{"v0.3.0", "v0.2.1", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.2.2", "v0.2.1", true},
		{"v0.2.1", "v0.2.1", false},
		{"v0.2.0", "v0.2.1", false},
		{"v0.1.0", "v0.2.1", false},
		{"v1.0.0", "0.2.1", true},
		{"0.3.0", "v0.2.1", true},
		{"dev", "v0.2.1", false},
		{"v0.3.0", "dev", false},
	}

	for _, tt := range tests {
		got := IsNewer(tt.latest, tt.current)
		if got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestBuildResult(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.1"

	result := buildResult("v0.3.0")
	if result == nil {
		t.Fatal("expected non-nil result for newer version")
	}

	if result.LatestVersion != "v0.3.0" {
		t.Errorf("LatestVersion = %q, want v0.3.0", result.LatestVersion)
	}

	if result.CurrentVersion != "v0.2.1" {
		t.Errorf("CurrentVersion = %q, want v0.2.1", result.CurrentVersion)
	}

	result = buildResult("v0.2.1")
	if result != nil {
		t.Errorf("expected nil result for same version, got %+v", result)
	}

	result = buildResult("v0.1.0")
	if result != nil {
		t.Errorf("expected nil result for older version, got %+v", result)
	}
}

func TestCheckForUpdate_SkipsDev(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "dev"

	result := CheckForUpdate(t.TempDir(), UpdateSettings{})
	if result != nil {
		t.Errorf("expected nil result for dev version, got %+v", result)
	}
}

func TestCheckForUpdate_UsesCache(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.0"
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update-check.json")

	cache := &UpdateCache{
		LatestVersion: "v0.3.0",
		CheckedAt:     time.Now(),
	}

	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}

	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	result := CheckForUpdate(dir, UpdateSettings{})
	if result == nil {
		t.Fatal("expected non-nil result from cache")
	}

	if result.LatestVersion != "v0.3.0" {
		t.Errorf("LatestVersion = %q, want v0.3.0", result.LatestVersion)
	}
}

func TestCheckForUpdate_CacheUpToDate(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.3.0"
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update-check.json")

	cache := &UpdateCache{
		LatestVersion: "v0.3.0",
		CheckedAt:     time.Now(),
	}

	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}

	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	result := CheckForUpdate(dir, UpdateSettings{})
	if result != nil {
		t.Errorf("expected nil result when up to date, got %+v", result)
	}
}

func TestUpdateCache_ReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "cache.json")

	info := &UpdateCache{
		LatestVersion: "v1.2.3",
		CheckedAt:     time.Now().Truncate(time.Second),
	}

	writeUpdateCache(path, info)

	got, err := readUpdateCache(path)
	if err != nil {
		t.Fatal(err)
	}

	if got.LatestVersion != info.LatestVersion {
		t.Errorf("LatestVersion = %q, want %q", got.LatestVersion, info.LatestVersion)
	}

	if !got.CheckedAt.Equal(info.CheckedAt) {
		t.Errorf("CheckedAt = %v, want %v", got.CheckedAt, info.CheckedAt)
	}
}

func TestCheckForUpdate_Disabled(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.0"
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update-check.json")

	// A fresh cache with a newer version would normally produce a result; the
	// Disabled switch must short-circuit before any cache/network read.
	cache := &UpdateCache{LatestVersion: "v0.3.0", CheckedAt: time.Now()}

	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}

	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	result := CheckForUpdate(dir, UpdateSettings{Disabled: true})
	if result != nil {
		t.Errorf("expected nil result when disabled, got %+v", result)
	}
}

func TestCheckForUpdate_IntervalControlsCacheFreshness(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.0"
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update-check.json")

	// Cache is two hours old: stale under the 1h default (which would then hit
	// the network) but fresh under an explicit 3h interval, so the cached newer
	// version is returned without any network access.
	cache := &UpdateCache{LatestVersion: "v0.3.0", CheckedAt: time.Now().Add(-2 * time.Hour)}

	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}

	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	result := CheckForUpdate(dir, UpdateSettings{Interval: 3 * time.Hour})
	if result == nil {
		t.Fatal("expected cached result with a wide interval")
	}

	if result.LatestVersion != "v0.3.0" {
		t.Errorf("LatestVersion = %q, want v0.3.0", result.LatestVersion)
	}
}

func TestCheckForUpdate_NoCache(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v999.0.0"
	dir := t.TempDir()

	// A version newer than any release yields no result; the fetch is stubbed so
	// the test never touches the network.
	stubFetch(t, func(string, time.Duration) (string, error) { return "v1.0.0", nil })

	result := CheckForUpdate(dir, UpdateSettings{})
	if result != nil {
		t.Errorf("expected nil result for version newer than any release, got %+v", result)
	}
}

// stubFetch replaces the release lookup seam for the duration of a test so no
// network access occurs, restoring the original on cleanup.
func stubFetch(t *testing.T, fn func(string, time.Duration) (string, error)) {
	t.Helper()

	orig := fetchLatest
	fetchLatest = fn

	t.Cleanup(func() { fetchLatest = orig })
}

// TestCheckForUpdate_CacheScopedToRepository is the #1290 regression: a fresh
// cache seeded for repository A must not be reported when the configured
// repository is B. Instead the check refetches from B and caches B's result.
func TestCheckForUpdate_CacheScopedToRepository(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.0"
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update-check.json")

	// Repository A's fresh cache advertises a newer release that must be ignored
	// once we switch to repository B.
	seed := &UpdateCache{
		LatestVersion: "v9.9.9",
		CheckedAt:     time.Now(),
		Repository:    "braw/repo-a",
	}

	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}

	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	var fetched string

	stubFetch(t, func(repository string, _ time.Duration) (string, error) {
		fetched = repository
		return "v0.3.0", nil
	})

	result := CheckForUpdate(dir, UpdateSettings{Repository: "canny/repo-b"})
	if result == nil {
		t.Fatal("expected repository B to be fetched, not repository A's cache")
	}

	if result.LatestVersion == "v9.9.9" {
		t.Fatalf("repository A's cached release %q was reused for repository B", result.LatestVersion)
	}

	if result.LatestVersion != "v0.3.0" {
		t.Errorf("LatestVersion = %q, want v0.3.0", result.LatestVersion)
	}

	if fetched != "canny/repo-b" {
		t.Errorf("fetched repository = %q, want canny/repo-b", fetched)
	}

	// The refreshed cache must now be scoped to repository B.
	got, err := readUpdateCache(cachePath)
	if err != nil {
		t.Fatalf("read refreshed cache: %v", err)
	}

	if got.Repository != "canny/repo-b" {
		t.Errorf("refreshed cache repository = %q, want canny/repo-b", got.Repository)
	}
}

// TestCheckForUpdate_LegacyCacheReusedForDefaultRepository documents the
// conservative compatibility behaviour: a legacy entry (no repository field) is
// treated as DefaultRepository, so it is reused when the effective repository is
// the default and no fetch occurs.
func TestCheckForUpdate_LegacyCacheReusedForDefaultRepository(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.0"
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update-check.json")

	// Legacy on-disk shape: latest_version + checked_at, no repository field.
	if err := os.WriteFile(cachePath, []byte(`{"latest_version":"v0.3.0","checked_at":"`+
		time.Now().Format(time.RFC3339Nano)+`"}`), 0o600); err != nil {
		t.Fatalf("write legacy cache: %v", err)
	}

	stubFetch(t, func(string, time.Duration) (string, error) {
		t.Fatal("legacy cache for the default repository must not trigger a fetch")
		return "", nil
	})

	result := CheckForUpdate(dir, UpdateSettings{})
	if result == nil {
		t.Fatal("expected the legacy default-repository cache to be reused")
	}

	if result.LatestVersion != "v0.3.0" {
		t.Errorf("LatestVersion = %q, want v0.3.0", result.LatestVersion)
	}
}

// TestCheckForUpdate_LegacyCacheRefreshedForConfiguredRepository proves the
// other half of the compatibility rule: a legacy entry (implicitly the default
// repository) is discarded when a non-default repository is configured, forcing
// a refresh rather than reusing the default repository's release.
func TestCheckForUpdate_LegacyCacheRefreshedForConfiguredRepository(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.0"
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update-check.json")

	if err := os.WriteFile(cachePath, []byte(`{"latest_version":"v9.9.9","checked_at":"`+
		time.Now().Format(time.RFC3339Nano)+`"}`), 0o600); err != nil {
		t.Fatalf("write legacy cache: %v", err)
	}

	fetched := false

	stubFetch(t, func(repository string, _ time.Duration) (string, error) {
		fetched = true

		if repository != "canny/repo-b" {
			t.Errorf("fetched repository = %q, want canny/repo-b", repository)
		}

		return "v0.3.0", nil
	})

	result := CheckForUpdate(dir, UpdateSettings{Repository: "canny/repo-b"})

	if !fetched {
		t.Fatal("configured non-default repository must force a refresh of a legacy cache")
	}

	if result == nil || result.LatestVersion != "v0.3.0" {
		t.Fatalf("expected the freshly fetched v0.3.0, got %+v", result)
	}
}
