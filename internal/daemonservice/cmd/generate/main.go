package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/d0ugal/graith/internal/daemonservice"
)

func main() {
	launchAgents := flag.String("launch-agents", "", "output directory for generated LaunchAgent plists")
	swift := flag.String("swift", "", "output path for generated Swift lookup source")

	flag.Parse()

	if *launchAgents == "" || *swift == "" {
		fmt.Fprintln(os.Stderr, "both --launch-agents and --swift are required")
		os.Exit(2)
	}

	if err := daemonservice.GenerateAssets(*launchAgents, *swift); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
