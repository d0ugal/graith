package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/d0ugal/graith/internal/atomicfile"
	"github.com/d0ugal/graith/internal/cipolicy"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("cipolicy", flag.ContinueOnError)
	inventoryPath := flags.String("inventory", cipolicy.DefaultInventoryPath, "P0 baseline inventory path")
	manifestPath := flags.String("manifest", cipolicy.DefaultManifestPath, "policy manifest path")
	outputPath := flags.String("output", "", "output path for generate (stdout when empty)")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 1 {
		return errors.New("usage: cipolicy [flags] generate|validate|digest")
	}

	command := flags.Arg(0)
	if command != "generate" && *outputPath != "" {
		return errors.New("-output is only valid with generate")
	}

	switch command {
	case "generate":
		manifest, err := cipolicy.GenerateFromInventoryPath(*inventoryPath)
		if err != nil {
			return err
		}

		data, err := manifest.MarshalCanonical()
		if err != nil {
			return err
		}

		generated, err := cipolicy.DecodeManifest("generated policy manifest", data)
		if err != nil {
			return err
		}

		if err := generated.Validate(); err != nil {
			return err
		}

		if *outputPath == "" {
			_, err = os.Stdout.Write(data)
			return err
		}

		return atomicfile.Write(*outputPath, data, 0o600)
	case "validate":
		manifest, err := cipolicy.ReadManifest(*manifestPath)
		if err != nil {
			return err
		}

		if err := cipolicy.ValidateCurrent(manifest, *inventoryPath); err != nil {
			return err
		}

		fmt.Printf("%s\n", manifest.PolicyDigest)

		return nil
	case "digest":
		manifest, err := cipolicy.ReadManifest(*manifestPath)
		if err != nil {
			return err
		}

		if err := manifest.Validate(); err != nil {
			return err
		}

		fmt.Printf("%s\n", manifest.PolicyDigest)

		return nil
	default:
		return fmt.Errorf("unknown command %q", flags.Arg(0))
	}
}
