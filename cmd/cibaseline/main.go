package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

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
	return runWithNow(args, time.Now)
}

func runWithNow(args []string, now func() time.Time) error {
	flags := flag.NewFlagSet("cibaseline", flag.ContinueOnError)
	repo := flags.String("repo", ".", "repository root")
	inventoryPath := flags.String("inventory", "internal/cibaseline/inventory.json", "inventory path")
	input := flags.String("input", "", "offline snapshot/evidence JSON")
	output := flags.String("output", "", "output path (stdout when empty)")
	repository := flags.String("repository", "d0ugal/graith", "GitHub owner/repository")
	maxElapsed := flags.Duration(
		"max-elapsed",
		cibaseline.DefaultCollectionMaxElapsed,
		"maximum elapsed time for a GitHub fetch",
	)
	maxRequests := flags.Int(
		"max-requests",
		cibaseline.DefaultCollectionMaxRequests,
		"maximum GitHub HTTP requests including retries",
	)
	maxRetries := flags.Int(
		"max-retries",
		cibaseline.DefaultCollectionMaxRetries,
		"maximum rate-limit retries across a GitHub fetch",
	)
	maturationDelay := flags.Duration(
		"maturation-delay",
		cibaseline.DefaultRunMaturationDelay,
		"delay between collection start and the workflow-run observation cutoff",
	)

	since := flags.String("since", "1", "RFC3339 start or day lookback (1-28; relative to -until when supplied)")
	until := flags.String("until", "", "optional whole-second RFC3339 end of the workflow-run created-time window")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 1 {
		return errors.New("usage: cibaseline [flags] generate|validate|fetch|collect|replay")
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
	case "collect":
		if *input == "" {
			return errors.New("-input is required")
		}

		inventory, err := readInventory(*inventoryPath)
		if err != nil {
			return err
		}

		snapshot, err := readSnapshot(*input)
		if err != nil {
			return err
		}

		evidence, err := cibaseline.Collect(snapshot, inventory)
		if err != nil {
			return err
		}

		return writeJSON(*output, evidence)
	case "fetch":
		window, err := cibaseline.ParseWindow(*since, *until, now())
		if err != nil {
			return err
		}

		inventory, err := readInventory(*inventoryPath)
		if err != nil {
			return err
		}

		collector := cibaseline.GitHubCollector{
			Token:           os.Getenv("GITHUB_TOKEN"),
			Client:          &http.Client{Timeout: 30 * time.Second},
			Now:             now,
			MaxElapsed:      *maxElapsed,
			MaxRequests:     *maxRequests,
			MaxRetries:      *maxRetries,
			MaturationDelay: *maturationDelay,
		}

		var snapshot cibaseline.GitHubSnapshot
		if window.ExplicitUntil {
			snapshot, err = collector.FetchWindow(context.Background(), *repository, window.Since, window.Until, inventory)
		} else {
			snapshot, err = collector.Fetch(context.Background(), *repository, window.Since, inventory)
		}

		if err != nil {
			return err
		}

		evidence, err := cibaseline.Collect(snapshot, inventory)
		if err != nil {
			return err
		}

		return writeJSON(*output, evidence)
	case "replay":
		if *input == "" {
			return errors.New("-input is required")
		}

		evidence, err := readEvidence(*input)
		if err != nil {
			return err
		}

		inventory, err := readInventory(*inventoryPath)
		if err != nil {
			return err
		}

		return evidence.Replay(inventory)
	default:
		return fmt.Errorf("unknown command %q", flags.Arg(0))
	}
}

func readEvidence(path string) (cibaseline.Evidence, error) {
	return readVersionedJSON[cibaseline.Evidence](path, "evidence", cibaseline.EvidenceSchemaVersion)
}

func readSnapshot(path string) (cibaseline.GitHubSnapshot, error) {
	return readVersionedJSON[cibaseline.GitHubSnapshot](path, "snapshot", cibaseline.SnapshotSchemaVersion)
}

func readVersionedJSON[T any](path, kind string, expectedSchema int) (T, error) {
	var value T

	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}

	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return value, fmt.Errorf("decode %s schema: %w", path, err)
	}

	if header.SchemaVersion != expectedSchema {
		return value, fmt.Errorf("unsupported %s schema %d", kind, header.SchemaVersion)
	}

	if err := decodeJSON(path, data, &value); err != nil {
		return value, err
	}

	return value, nil
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
