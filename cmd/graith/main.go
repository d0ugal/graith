// cmd/graith/main.go
package main

import (
	"os"

	"github.com/d0ugal/graith/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
