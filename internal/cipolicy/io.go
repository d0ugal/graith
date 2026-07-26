package cipolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/d0ugal/graith/internal/cibaseline"
)

func ReadInventory(path string) (cibaseline.Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cibaseline.Inventory{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var inventory cibaseline.Inventory
	if err := decoder.Decode(&inventory); err != nil {
		return cibaseline.Inventory{}, fmt.Errorf("decode %s: %w", path, err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return cibaseline.Inventory{}, fmt.Errorf("decode %s: trailing JSON value", path)
		}

		return cibaseline.Inventory{}, fmt.Errorf("decode %s: %w", path, err)
	}

	return inventory, nil
}

func GenerateFromInventoryPath(path string) (Manifest, error) {
	inventory, err := ReadInventory(path)
	if err != nil {
		return Manifest{}, err
	}

	return FromInventory(inventory)
}

func ValidateCurrent(manifest Manifest, inventoryPath string) error {
	if err := manifest.Validate(); err != nil {
		return err
	}

	inventory, err := ReadInventory(inventoryPath)
	if err != nil {
		return err
	}

	if err := inventory.Validate(); err != nil {
		return fmt.Errorf("validate P0 inventory: %w", err)
	}

	if err := validateRequiredContexts(manifest, inventory); err != nil {
		return err
	}

	generated, err := FromInventory(inventory)
	if err != nil {
		return err
	}

	generatedBytes, err := generated.MarshalCanonical()
	if err != nil {
		return err
	}

	manifestBytes, err := manifest.MarshalCanonical()
	if err != nil {
		return err
	}

	if !bytes.Equal(manifestBytes, generatedBytes) {
		generatedDigest, digestErr := generated.Digest()
		if digestErr != nil {
			return digestErr
		}

		return fmt.Errorf("policy manifest is stale: got %s want %s", manifest.PolicyDigest, generatedDigest)
	}

	return nil
}

func validateRequiredContexts(manifest Manifest, inventory cibaseline.Inventory) error {
	requiredChecks := map[string]string{}

	for _, mode := range manifest.Modes {
		for _, coordinate := range mode.Coordinates {
			if coordinate.Requiredness == "required" {
				requiredChecks[coordinate.GitHubName] = coordinate.ID
			}
		}
	}

	inventoryContexts := map[string]bool{}
	for _, context := range inventory.RequiredContexts {
		inventoryContexts[context] = true
	}

	if len(requiredChecks) != len(inventoryContexts) {
		return fmt.Errorf("required contexts mismatch: manifest has %d required coordinates, P0 inventory has %d required contexts", len(requiredChecks), len(inventoryContexts))
	}

	for context := range inventoryContexts {
		if requiredChecks[context] == "" {
			return fmt.Errorf("required contexts mismatch: P0 context %q is not required by policy manifest", context)
		}
	}

	for githubName, coordinateID := range requiredChecks {
		if !inventoryContexts[githubName] {
			return fmt.Errorf("required contexts mismatch: policy coordinate %s uses unprotected context %q", coordinateID, githubName)
		}
	}

	return nil
}
