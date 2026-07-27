package cipolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestWorkflowGoRunCommandReferencesExist(t *testing.T) {
	repoRoot := p11RepoRoot()
	workflowDir := filepath.Join(repoRoot, ".github", "workflows")

	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatalf("read workflow directory: %v", err)
	}

	commandRef := regexp.MustCompile(`\bgo\s+run\s+\./(cmd/[A-Za-z0-9_-]+)\b`)

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		workflowPath := filepath.Join(workflowDir, entry.Name())
		workflow := readPolicyFile(t, workflowPath)

		for _, match := range commandRef.FindAllStringSubmatch(workflow, -1) {
			commandPath := filepath.Join(repoRoot, match[1])

			info, err := os.Stat(commandPath)
			if err != nil {
				t.Fatalf("%s references missing Go command %s: %v", workflowPath, match[1], err)
			}

			if !info.IsDir() {
				t.Fatalf("%s references Go command %s, but %s is not a directory", workflowPath, match[1], commandPath)
			}
		}
	}
}
