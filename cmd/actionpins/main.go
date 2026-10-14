// Command actionpins regenerates the action SHA expectations that the
// internal/ciworkflow policy tests compare workflows against.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/d0ugal/graith/internal/ciworkflow"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("actionpins", flag.ContinueOnError)
	repo := flags.String("repo", ".", "repository root")
	check := flags.Bool("check", false, "fail instead of writing when the committed pins are stale")

	if err := flags.Parse(args); err != nil {
		return err //nolint:wrapcheck // flag error is already descriptive
	}

	source, err := os.ReadFile(filepath.Join(*repo, ciworkflow.ActionPinsSourceWorkflow))
	if err != nil {
		return fmt.Errorf("read source workflow: %w", err)
	}

	pins, err := ciworkflow.ExtractActionPins(source)
	if err != nil {
		return fmt.Errorf("%s: %w", ciworkflow.ActionPinsSourceWorkflow, err)
	}

	want, err := ciworkflow.RenderActionPins(pins)
	if err != nil {
		return fmt.Errorf("render pins: %w", err)
	}

	path := filepath.Join(*repo, ciworkflow.ActionPinsPath)

	have, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", ciworkflow.ActionPinsPath, readErr)
	}

	if bytes.Equal(have, want) {
		_, _ = fmt.Fprintln(stdout, "action pins up to date")

		return nil
	}

	if *check {
		return fmt.Errorf("%s is stale; run `make action-pins`", ciworkflow.ActionPinsPath)
	}

	if err := os.WriteFile(path, want, 0o644); err != nil { //nolint:gosec // committed, non-secret file
		return fmt.Errorf("write %s: %w", ciworkflow.ActionPinsPath, err)
	}

	_, _ = fmt.Fprintf(stdout, "updated %s\n", ciworkflow.ActionPinsPath)

	return nil
}
