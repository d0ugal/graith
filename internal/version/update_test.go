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

	result := CheckForUpdate(t.TempDir())
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

	result := CheckForUpdate(dir)
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

	result := CheckForUpdate(dir)
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

func TestCheckForUpdate_NoCache(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v999.0.0"
	dir := t.TempDir()

	// With no cache and a version newer than any release, result should be nil
	// (whether the fetch succeeds or fails, there's no newer version)
	result := CheckForUpdate(dir)
	if result != nil {
		t.Errorf("expected nil result for version newer than any release, got %+v", result)
	}
}
