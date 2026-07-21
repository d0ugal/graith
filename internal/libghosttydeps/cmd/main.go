package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/d0ugal/graith/internal/libghosttydeps"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		usage()
	}

	root := "."
	if len(os.Args) == 3 {
		root = os.Args[2]
	}

	absolute, err := filepath.Abs(root)
	if err != nil {
		fatal(err)
	}

	switch os.Args[1] {
	case "verify":
		err = libghosttydeps.Verify(absolute)
	case "verify-generated":
		err = libghosttydeps.VerifyGenerated(absolute)
	case "generate":
		err = libghosttydeps.Generate(context.Background(), absolute)
	case "accept-license-reviews":
		err = acceptLicenseReviews(absolute)
	default:
		usage()
	}

	if err != nil {
		fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run ./internal/libghosttydeps/cmd verify|verify-generated|generate|accept-license-reviews [repository-root]")
	os.Exit(2)
}

func acceptLicenseReviews(root string) error {
	path := filepath.Join(root, libghosttydeps.LockFilename)

	lock, err := libghosttydeps.LoadLock(path)
	if err != nil {
		return err
	}

	libghosttydeps.AcceptLicenseReviews(&lock)

	return libghosttydeps.WriteLock(path, lock)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
