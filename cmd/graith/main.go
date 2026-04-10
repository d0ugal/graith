// cmd/graith/main.go
package main

import (
	"os"

	"github.com/dougalmatthews/graith/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
