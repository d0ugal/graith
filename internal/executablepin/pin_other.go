//go:build !linux

package executablepin

import (
	"errors"
	"os"
)

func SealedCopy(*os.File, int64, string) (*os.File, error) {
	return nil, errors.New("sealed executable images are unsupported on this platform")
}

func Validate(*os.File, int64) error {
	return errors.New("sealed executable images are unsupported on this platform")
}
