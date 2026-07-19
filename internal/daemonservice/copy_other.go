//go:build !darwin

package daemonservice

import (
	"context"
	"os"
)

func copySignedBundle(ctx context.Context, source, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return os.CopyFS(destination, os.DirFS(source))
}
