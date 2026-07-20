//go:build darwin

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: renameexcl <source> <destination>")
		os.Exit(2)
	}

	if err := unix.RenameatxNp(
		unix.AT_FDCWD,
		os.Args[1],
		unix.AT_FDCWD,
		os.Args[2],
		unix.RENAME_EXCL,
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
