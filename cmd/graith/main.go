// cmd/graith/main.go
package main

import (
	"fmt"
	"os"

	"github.com/d0ugal/graith/internal/cli"
	"github.com/d0ugal/graith/internal/pty"
)

const nativeTerminalSelfTestArg = "--graith-internal-libghostty-self-test"

func main() {
	if len(os.Args) == 2 && os.Args[1] == nativeTerminalSelfTestArg {
		if err := pty.RunNativeTerminalSelfTest(); err != nil {
			fmt.Fprintln(os.Stderr, "libghostty native self-test failed")
			os.Exit(70)
		}

		fmt.Println("libghostty native self-test passed")

		return
	}

	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
