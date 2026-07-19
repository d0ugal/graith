//go:build darwin

package daemonservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const signatureCommandTimeout = 10 * time.Second

func VerifyDarwinSignature(path string) (SignatureInfo, error) {
	return VerifyDarwinSignatureContext(context.Background(), path)
}

func VerifyDarwinSignatureContext(parent context.Context, path string) (SignatureInfo, error) {
	ctx, cancel := context.WithTimeout(parent, signatureCommandTimeout)
	defer cancel()

	if output, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "--verbose=2", path).CombinedOutput(); err != nil {
		return SignatureInfo{}, fmt.Errorf("codesign verify: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	ctx, cancel = context.WithTimeout(parent, signatureCommandTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "/usr/bin/codesign", "-d", "--verbose=4", "-r-", path).CombinedOutput()
	if err != nil {
		return SignatureInfo{}, fmt.Errorf("codesign details: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	info := parseCodesignDetails(output)
	if info.Identifier == "" || info.Requirement == "" {
		return SignatureInfo{}, errors.New("codesign output omitted identifier or designated requirement")
	}

	return info, nil
}

func VerifyDarwinDistributionContext(parent context.Context, path string) error {
	commands := [][]string{
		{"/usr/bin/xcrun", "stapler", "validate", path},
		{"/usr/sbin/spctl", "--assess", "--type", "execute", "--verbose=2", path},
	}
	for _, command := range commands {
		ctx, cancel := context.WithTimeout(parent, signatureCommandTimeout)
		output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()

		cancel()

		if err != nil {
			return fmt.Errorf("%s: %w (%s)", command[0], err, strings.TrimSpace(string(output)))
		}
	}

	return nil
}

func parseCodesignDetails(output []byte) SignatureInfo {
	info := SignatureInfo{}

	for _, line := range bytes.Split(output, []byte{'\n'}) {
		text := string(line)
		switch {
		case strings.HasPrefix(text, "Identifier="):
			info.Identifier = strings.TrimPrefix(text, "Identifier=")
		case strings.HasPrefix(text, "TeamIdentifier="):
			info.TeamID = strings.TrimPrefix(text, "TeamIdentifier=")
		case strings.HasPrefix(text, "designated => "):
			info.Requirement = strings.TrimPrefix(text, "designated => ")
		case strings.HasPrefix(text, "# designated => "):
			// Ad-hoc signatures use a cdhash requirement and prefix the line
			// with "#". Development bundles still need that exact requirement
			// recorded and revalidated even though they have no Team ID.
			info.Requirement = strings.TrimPrefix(text, "# designated => ")
		}
	}

	return info
}
