package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/d0ugal/graith/internal/atomicfile"
	"github.com/d0ugal/graith/internal/cibaseline"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("cibaseline", flag.ContinueOnError)
	repo := flags.String("repo", ".", "repository root")
	inventoryPath := flags.String("inventory", "internal/cibaseline/inventory.json", "inventory path")
	output := flags.String("output", "", "output path (stdout when empty)")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 1 {
		return errors.New("usage: cibaseline [flags] generate|validate")
	}

	switch flags.Arg(0) {
	case "generate":
		inventory, err := cibaseline.BuildInventory(*repo)
		if err != nil {
			return err
		}

		return writeJSON(*output, inventory)
	case "validate":
		committed, err := readInventory(*inventoryPath)
		if err != nil {
			return err
		}

		return cibaseline.ValidateCurrent(*repo, committed)
	default:
		return fmt.Errorf("unknown command %q; supported commands are generate and validate", flags.Arg(0))
	}
}

func readInventory(path string) (cibaseline.Inventory, error) {
	var inventory cibaseline.Inventory

	err := readJSON(path, &inventory)

	return inventory, err
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return decodeJSON(path, data, value)
}

func decodeJSON(path string, data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: trailing JSON value", path)
		}

		return fmt.Errorf("decode %s: %w", path, err)
	}

	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')
	if path == "" {
		_, err = os.Stdout.Write(data)
		return err
	}

	return atomicfile.Write(path, data, 0o600)
}
