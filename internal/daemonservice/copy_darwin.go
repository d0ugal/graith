//go:build darwin

package daemonservice

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func copySignedBundle(parent context.Context, source, destination string) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, "/usr/bin/ditto", "--rsrc", "--extattr", "--noqtn", source, destination).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ditto: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	return nil
}
