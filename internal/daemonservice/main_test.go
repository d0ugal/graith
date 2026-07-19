package daemonservice

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve test working directory: %v\n", err)
		os.Exit(1)
	}

	tempRoot, err := os.MkdirTemp(workingDirectory, ".graith-daemonservice-braw-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create secure test temp root: %v\n", err)
		os.Exit(1)
	}

	previousTemp, hadPreviousTemp := os.LookupEnv("TMPDIR")

	if err := os.Setenv("TMPDIR", tempRoot); err != nil {
		fmt.Fprintf(os.Stderr, "set test TMPDIR: %v\n", err)

		_ = os.RemoveAll(tempRoot)

		os.Exit(1)
	}

	code := m.Run()

	if hadPreviousTemp {
		_ = os.Setenv("TMPDIR", previousTemp)
	} else {
		_ = os.Unsetenv("TMPDIR")
	}

	if err := os.RemoveAll(tempRoot); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "remove secure test temp root: %v\n", err)

		code = 1
	}

	os.Exit(code)
}
