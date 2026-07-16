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

// TestCheckForUpdate_CacheScopedToRepository is the regression for issue #1290:
// a fresh cache produced by repository A must not be served when a different
// repository B is now configured. Before the fix the cache carried no repository
// and any fresh entry was reused for the whole interval regardless of source.
func TestCheckForUpdate_CacheScopedToRepository(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.0"

	origFetch := fetchLatest
	defer func() { fetchLatest = origFetch }()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update-check.json")

	// A FRESH cache produced by repository A, advertising a newer release.
	cache := &UpdateCache{
		LatestVersion: "v9.9.9",
		CheckedAt:     time.Now(),
		Repository:    "owner-a/repo-a",
	}

	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}

	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	// Checking repository B must not reuse A's entry; capture which repo the fetch
	// path is asked about instead of touching the network.
	var fetched string

	fetchLatest = func(repo string, _ time.Duration) (string, error) {
		fetched = repo

		return "v0.4.0", nil
	}

	result := CheckForUpdate(dir, UpdateSettings{Repository: "owner-b/repo-b"})

	if fetched != "owner-b/repo-b" {
		t.Fatalf("expected a fresh fetch for repository B, got fetched=%q", fetched)
	}

	if result == nil || result.LatestVersion != "v0.4.0" {
		t.Fatalf("expected repository B's freshly fetched result v0.4.0, got %+v", result)
	}

	if result.LatestVersion == "v9.9.9" {
		t.Fatal("repository A's cached result must not be served for repository B")
	}

	// The cache is rewritten and now scoped to repository B.
	got, err := readUpdateCache(cachePath)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}

	if got.Repository != "owner-b/repo-b" || got.LatestVersion != "v0.4.0" {
		t.Errorf("cache after B check = %+v; want repo owner-b/repo-b, version v0.4.0", got)
	}
}

// TestCheckForUpdate_LegacyCacheTreatedAsDefaultRepository pins the conservative
// compatibility behavior for legacy cache files with no repository field: they
// are reused only for the default repository (all an older binary ever queried),
// and re-fetched for any other configured repository.
func TestCheckForUpdate_LegacyCacheTreatedAsDefaultRepository(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.0"

	origFetch := fetchLatest
	defer func() { fetchLatest = origFetch }()

	writeLegacy := func(t *testing.T) string {
		t.Helper()

		dir := t.TempDir()
		// A legacy record: latest_version + checked_at, no repository field.
		body := `{"latest_version":"v0.3.0","checked_at":"` + time.Now().Format(time.RFC3339Nano) + `"}`
		if err := os.WriteFile(filepath.Join(dir, "update-check.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		return dir
	}

	t.Run("default repo reuses legacy entry", func(t *testing.T) {
		dir := writeLegacy(t)

		fetchLatest = func(string, time.Duration) (string, error) {
			t.Fatal("must not fetch: a legacy entry maps to the default repository")

			return "", nil
		}

		result := CheckForUpdate(dir, UpdateSettings{})
		if result == nil || result.LatestVersion != "v0.3.0" {
			t.Fatalf("expected reused legacy result v0.3.0, got %+v", result)
		}
	})

	t.Run("non-default repo ignores legacy entry", func(t *testing.T) {
		dir := writeLegacy(t)

		var fetched string

		fetchLatest = func(repo string, _ time.Duration) (string, error) {
			fetched = repo

			return "v0.5.0", nil
		}

		result := CheckForUpdate(dir, UpdateSettings{Repository: "owner-b/repo-b"})

		if fetched != "owner-b/repo-b" {
			t.Fatalf("expected a fetch for the non-default repository, got fetched=%q", fetched)
		}

		if result == nil || result.LatestVersion != "v0.5.0" {
			t.Fatalf("expected freshly fetched v0.5.0, got %+v", result)
		}
	})
}

func TestCheckForUpdate_NoCache(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v999.0.0"
	dir := t.TempDir()

	// With no cache and a version newer than any release, result should be nil
	// (whether the fetch succeeds or fails, there's no newer version)
	result := CheckForUpdate(dir, UpdateSettings{})
	if result != nil {
		t.Errorf("expected nil result for version newer than any release, got %+v", result)
	}
}
