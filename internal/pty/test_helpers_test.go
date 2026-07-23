package pty

import (
	"errors"
	"io"
	"os"
	"testing"
)

func closePTYTestResource(t *testing.T, closer io.Closer) {
	t.Helper()

	if err := closer.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close test resource: %v", err)
	}
}
