package version

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	checkInterval = time.Hour
	githubRepo    = "d0ugal/graith"
	httpTimeout   = 5 * time.Second
)

type UpdateCache struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
}

type UpdateResult struct {
	LatestVersion  string
	CurrentVersion string
}

func CheckForUpdate(cacheDir string) *UpdateResult {
	if Version == "dev" {
		return nil
	}

	cachePath := filepath.Join(cacheDir, "update-check.json")

	if info, err := readUpdateCache(cachePath); err == nil {
		if time.Since(info.CheckedAt) < checkInterval {
			return buildResult(info.LatestVersion)
		}
	}

	latest, err := fetchLatestVersion()
	if err != nil {
		return nil
	}

	writeUpdateCache(cachePath, &UpdateCache{
		LatestVersion: latest,
		CheckedAt:     time.Now(),
	})

	return buildResult(latest)
}

func buildResult(latest string) *UpdateResult {
	if !IsNewer(latest, Version) {
		return nil
	}
	return &UpdateResult{
		LatestVersion:  latest,
		CurrentVersion: Version,
	}
}

func readUpdateCache(path string) (*UpdateCache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info UpdateCache
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func writeUpdateCache(path string, info *UpdateCache) {
	data, err := json.Marshal(info)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, data, 0o600)
}

func fetchLatestVersion() (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	client := &http.Client{Timeout: httpTimeout}

	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github API returned %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	return release.TagName, nil
}

func IsNewer(latest, current string) bool {
	latestParts := parseVersion(latest)
	currentParts := parseVersion(current)
	if latestParts == nil || currentParts == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if latestParts[i] > currentParts[i] {
			return true
		}
		if latestParts[i] < currentParts[i] {
			return false
		}
	}
	return false
}

func parseVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	result := make([]int, 3)
	for i, p := range parts {
		p, _, _ = strings.Cut(p, "-")
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		result[i] = n
	}
	return result
}
