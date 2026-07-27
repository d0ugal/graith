package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/d0ugal/graith/internal/libghosttyarchive"
)

func main() {
	os.Exit(run(filepath.Base(os.Args[0]), os.Args[1:], os.Stderr))
}

func run(command string, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr, command)
		return 2
	}

	var err error

	switch args[0] {
	case "inspect":
		if len(args) != 2 {
			usage(stderr, command)
			return 2
		}

		err = libghosttyarchive.Inspect(args[1])
	case "pack":
		if len(args) != 3 {
			usage(stderr, command)
			return 2
		}

		err = libghosttyarchive.Pack(args[1], args[2])
	case "test":
		if len(args) != 1 {
			usage(stderr, command)
			return 2
		}

		err = libghosttyarchive.Regression()
	default:
		usage(stderr, command)
		return 2
	}

	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	return 0
}

func usage(stderr io.Writer, command string) {
	_, _ = fmt.Fprintf(stderr, "usage: %s inspect <archive>\n", command)
	_, _ = fmt.Fprintf(stderr, "       %s pack <source> <archive>\n", command)
	_, _ = fmt.Fprintf(stderr, "       %s test\n", command)
}
