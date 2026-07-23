package main

import (
	"fmt"
	"os"
	"time"

	"github.com/d0ugal/graith/internal/architecture"
)

func main() {
	manifestFile, err := os.Open("internal/architecture/manifest.json")
	if err != nil {
		fatal(err)
	}

	manifest, err := architecture.Load(manifestFile)
	_ = manifestFile.Close()

	if err != nil {
		fatal(err)
	}

	packages, err := architecture.Discover("go", ".")
	if err != nil {
		fatal(err)
	}

	violations, err := architecture.Check(manifest, packages, time.Now().UTC())
	if err != nil {
		fatal(err)
	}

	failed := false
	exceptions := 0

	for _, violation := range violations {
		fmt.Println(architecture.Format(violation))

		if violation.Exception == "" {
			failed = true
		} else {
			exceptions++
		}
	}

	fmt.Printf("architecture: %d packages checked, %d forbidden edges, %d active exceptions\n", len(packages), len(violations)-exceptions, exceptions)

	if failed {
		os.Exit(1)
	}
}

func fatal(err error) { fmt.Fprintf(os.Stderr, "architecturecheck: %v\n", err); os.Exit(1) }
