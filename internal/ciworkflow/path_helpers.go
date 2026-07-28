package ciworkflow

import (
	"path/filepath"
	"slices"
	"strings"
)

func canonicalChangedFiles(changedFiles []string) []string {
	normalized := make([]string, 0, len(changedFiles))
	for _, path := range changedFiles {
		normalized = append(normalized, normalizeChangedPath(path))
	}

	return sortedStrings(normalized)
}

func normalizeChangedPath(path string) string {
	return filepath.ToSlash(strings.TrimSuffix(path, "\r"))
}

func invalidChangedPath(path string) bool {
	if strings.TrimSpace(path) != path ||
		strings.HasPrefix(path, "/") ||
		filepath.IsAbs(path) {
		return true
	}

	for _, component := range strings.Split(path, "/") {
		if component == "." || component == ".." {
			return true
		}
	}

	return false
}

func isCIWorkflowPath(path string) bool {
	return workflowRuleMatches(ciWorkflowRules, path)
}

func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	unique := make(map[string]bool, len(values))
	for _, value := range values {
		unique[value] = true
	}

	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}

	slices.Sort(result)

	return result
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}
