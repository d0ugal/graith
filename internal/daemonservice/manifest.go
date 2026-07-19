// Package daemonservice owns the platform-neutral contracts for Graith's
// app-associated per-user daemon. Darwin-specific registration is isolated in
// build-tagged files; other platforms retain direct spawning.
package daemonservice

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	DefaultSlot          = "default"
	ControllerExecutable = "graith-service-controller"
	DaemonExecutable     = "gr"
	AppBundleName        = "Graith.app"
)

//go:embed service_manifest.json
var manifestJSON []byte

type Manifest struct {
	Schema           int    `json:"schema"`
	BundleIdentifier string `json:"bundle_identifier"`
	DefaultLabel     string `json:"default_label"`
	NamedLabelPrefix string `json:"named_label_prefix"`
	ProfileSlots     int    `json:"profile_slots"`
}

type Definition struct {
	Slot      string
	Label     string
	PlistName string
}

var compiledManifest = mustParseManifest(manifestJSON)

func mustParseManifest(data []byte) Manifest {
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		panic(fmt.Sprintf("parse embedded daemon service manifest: %v", err))
	}

	if manifest.Schema != 1 || manifest.BundleIdentifier == "" ||
		manifest.DefaultLabel == "" || manifest.NamedLabelPrefix == "" ||
		manifest.ProfileSlots != 64 {
		panic("embedded daemon service manifest has invalid required fields")
	}

	return manifest
}

func ServiceManifest() Manifest { return compiledManifest }

func Definitions() []Definition {
	manifest := ServiceManifest()
	definitions := make([]Definition, 0, manifest.ProfileSlots+1)
	definitions = append(definitions, Definition{
		Slot:      DefaultSlot,
		Label:     manifest.DefaultLabel,
		PlistName: manifest.DefaultLabel + ".plist",
	})

	for slot := range manifest.ProfileSlots {
		name := fmt.Sprintf("%02d", slot)
		label := manifest.NamedLabelPrefix + name
		definitions = append(definitions, Definition{Slot: name, Label: label, PlistName: label + ".plist"})
	}

	return definitions
}

func DefinitionForSlot(slot string) (Definition, error) {
	if slot == DefaultSlot {
		return Definitions()[0], nil
	}

	if len(slot) != 2 || slot[0] < '0' || slot[0] > '9' || slot[1] < '0' || slot[1] > '9' {
		return Definition{}, fmt.Errorf("invalid daemon service slot %q", slot)
	}

	index, err := strconv.Atoi(slot)
	if err != nil || index < 0 || index >= ServiceManifest().ProfileSlots {
		return Definition{}, fmt.Errorf("invalid daemon service slot %q", slot)
	}

	return Definitions()[index+1], nil
}

func ValidateMarker(label, slot string) (Definition, error) {
	definition, err := DefinitionForSlot(slot)
	if err != nil {
		return Definition{}, err
	}

	if label != definition.Label {
		return Definition{}, fmt.Errorf("daemon service label %q does not match slot %q", label, slot)
	}

	return definition, nil
}

func ProfileForDefinition(definition Definition, requestedProfile string) error {
	if definition.Slot == DefaultSlot {
		if requestedProfile != "" {
			return errors.New("default daemon service cannot start a named profile")
		}

		return nil
	}

	if requestedProfile == "" || requestedProfile == "default" || strings.TrimSpace(requestedProfile) != requestedProfile {
		return fmt.Errorf("named daemon service slot %q requires a canonical profile", definition.Slot)
	}

	return nil
}
