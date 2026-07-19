//go:build !darwin

package daemonservice

import (
	"context"
	"errors"
)

func VerifyDarwinSignature(string) (SignatureInfo, error) {
	return SignatureInfo{}, errors.New("darwin code signature verification is unavailable on this platform")
}

func VerifyDarwinSignatureContext(context.Context, string) (SignatureInfo, error) {
	return SignatureInfo{}, errors.New("darwin code signature verification is unavailable on this platform")
}

func VerifyDarwinDistributionContext(context.Context, string) error {
	return errors.New("darwin distribution verification is unavailable on this platform")
}
