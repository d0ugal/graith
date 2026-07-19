package daemonservice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefinitionsAreCompleteAndUnique(t *testing.T) {
	t.Parallel()

	definitions := Definitions()
	if got, want := len(definitions), 65; got != want {
		t.Fatalf("Definitions() length = %d, want %d", got, want)
	}

	labels := make(map[string]bool, len(definitions))

	plists := make(map[string]bool, len(definitions))
	for index, definition := range definitions {
		if labels[definition.Label] {
			t.Errorf("duplicate label %q", definition.Label)
		}

		if plists[definition.PlistName] {
			t.Errorf("duplicate plist %q", definition.PlistName)
		}

		labels[definition.Label] = true
		plists[definition.PlistName] = true

		if index == 0 {
			if definition.Slot != DefaultSlot {
				t.Errorf("first definition slot = %q, want default", definition.Slot)
			}

			continue
		}

		wantSlot := definitions[index].Label[len(ServiceManifest().NamedLabelPrefix):]
		if definition.Slot != wantSlot || len(definition.Slot) != 2 {
			t.Errorf("definition %d slot = %q, label suffix = %q", index, definition.Slot, wantSlot)
		}
	}
}

func TestGeneratedAssetsHaveSafeStaticContract(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	agents := filepath.Join(root, "LaunchAgents")

	swift := filepath.Join(root, "Services.generated.swift")
	if err := GenerateAssets(agents, swift); err != nil {
		t.Fatalf("GenerateAssets() = %v", err)
	}

	entries, err := os.ReadDir(agents)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := len(entries), 65; got != want {
		t.Fatalf("generated plist count = %d, want %d", got, want)
	}

	for _, definition := range Definitions() {
		data, err := os.ReadFile(filepath.Join(agents, definition.PlistName))
		if err != nil {
			t.Fatalf("read %s: %v", definition.PlistName, err)
		}

		text := string(data)
		for _, forbidden := range []string{"KeepAlive", "RunAtLoad", "EnvironmentVariables", "<key>Sockets</key>", "MachServices"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s contains forbidden %s", definition.PlistName, forbidden)
			}
		}

		for _, required := range []string{definition.Label, definition.Slot, "Contents/MacOS/gr", "--internal-service-label", "--internal-service-slot"} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing %q", definition.PlistName, required)
			}
		}
	}

	generatedSwift, err := os.ReadFile(swift)
	if err != nil {
		t.Fatal(err)
	}

	for _, definition := range Definitions() {
		if !strings.Contains(string(generatedSwift), definition.PlistName) {
			t.Errorf("generated Swift missing %s", definition.PlistName)
		}
	}
}

func TestValidateMarker(t *testing.T) {
	t.Parallel()

	definition, err := DefinitionForSlot("07")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateMarker(definition.Label, definition.Slot); err != nil {
		t.Fatalf("valid marker rejected: %v", err)
	}

	if _, err := ValidateMarker(ServiceManifest().DefaultLabel, definition.Slot); err == nil {
		t.Fatal("mismatched label accepted")
	}

	if _, err := DefinitionForSlot("64"); err == nil {
		t.Fatal("out-of-range slot accepted")
	}
}
