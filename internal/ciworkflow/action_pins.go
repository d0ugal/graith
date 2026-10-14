package ciworkflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
)

// ActionPinsPath is the committed expectation file, relative to the repository
// root, that the policy tests compare workflow `uses:` lines against.
const ActionPinsPath = "internal/ciworkflow/testdata/action_pins.json"

// ActionPinsSourceWorkflow is the workflow whose `uses:` lines are the source of
// truth for the expected pins. Other workflows must agree with it; the policy
// tests fail when they drift.
const ActionPinsSourceWorkflow = ".github/workflows/regen.yml"

// PinnedActions lists the actions whose SHAs the policy tests pin strictly.
var PinnedActions = []string{
	"actions/checkout",
	"actions/download-artifact",
	"actions/upload-artifact",
}

var usesLinePattern = regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@([0-9a-f]{40})\b`)

// ExtractActionPins returns the pinned SHA of each PinnedActions entry found in
// workflow source. An action pinned to more than one SHA, or missing, is an error.
func ExtractActionPins(workflow []byte) (map[string]string, error) {
	found := map[string]string{}

	for _, match := range usesLinePattern.FindAllSubmatch(workflow, -1) {
		name, sha := string(match[1]), string(match[2])
		if !isPinnedAction(name) {
			continue
		}

		if prev, ok := found[name]; ok && prev != sha {
			return nil, fmt.Errorf("%s is pinned to both %s and %s", name, prev, sha)
		}

		found[name] = sha
	}

	for _, name := range PinnedActions {
		if _, ok := found[name]; !ok {
			return nil, fmt.Errorf("%s is not pinned by a full-SHA uses line", name)
		}
	}

	return found, nil
}

func isPinnedAction(name string) bool {
	for _, pinned := range PinnedActions {
		if pinned == name {
			return true
		}
	}

	return false
}

// RenderActionPins renders pins as the canonical committed JSON document.
func RenderActionPins(pins map[string]string) ([]byte, error) {
	names := make([]string, 0, len(pins))
	for name := range pins {
		names = append(names, name)
	}

	sort.Strings(names)

	var buf bytes.Buffer

	buf.WriteString("{\n")

	for i, name := range names {
		key, err := json.Marshal(name)
		if err != nil {
			return nil, fmt.Errorf("marshal action name: %w", err)
		}

		value, err := json.Marshal(pins[name])
		if err != nil {
			return nil, fmt.Errorf("marshal action pin: %w", err)
		}

		buf.WriteString("  ")
		buf.Write(key)
		buf.WriteString(": ")
		buf.Write(value)

		if i < len(names)-1 {
			buf.WriteString(",")
		}

		buf.WriteString("\n")
	}

	buf.WriteString("}\n")

	return buf.Bytes(), nil
}

// ReadActionPins loads the committed pin expectations.
func ReadActionPins(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read action pins: %w", err)
	}

	pins := map[string]string{}
	if err := json.Unmarshal(data, &pins); err != nil {
		return nil, fmt.Errorf("parse action pins: %w", err)
	}

	return pins, nil
}
