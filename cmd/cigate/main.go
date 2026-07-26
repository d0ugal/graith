package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/atomicfile"
	"github.com/d0ugal/graith/internal/cigate"
	"github.com/d0ugal/graith/internal/cipolicy"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("cigate", flag.ContinueOnError)
	inputPath := flags.String("input", "", "input JSON bundle")
	outputPath := flags.String("output", "", "output path (stdout when empty)")
	replayStorePath := flags.String("replay-store", "", "file-backed replay store for evaluate")
	trustedConfigPath := flags.String("trusted-config", "", "trusted evaluator config JSON")
	configDigest := flags.String("config-digest", "", "expected trusted config digest")
	trustedPolicyPath := flags.String("trusted-policy", "", "trusted policy manifest path")
	policyDigest := flags.String("policy-digest", "", "expected trusted policy digest")
	evaluatorVersion := flags.String("evaluator-version", "", "trusted evaluator version")
	releaseDigest := flags.String("release-digest", "", "trusted digest-pinned release digest")
	evaluatorDigest := flags.String("evaluator-digest", "", "trusted evaluator source digest")
	nowValue := flags.String("test-clock", "", "RFC3339 evaluator-owned test clock override")
	legacyNowValue := flags.String("now", "", "deprecated alias for -test-clock")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 1 {
		return errors.New("usage: cigate [flags] evaluate|live-proof")
	}

	command := flags.Arg(0)
	evaluateFailure := func(input cigate.EvaluationInput, evalErr error) error {
		return writeFailedEvaluation(*outputPath, input, time.Now().UTC(), evalErr)
	}

	if *inputPath == "" {
		err := errors.New("-input is required")
		if command == "evaluate" {
			return evaluateFailure(cigate.EvaluationInput{}, err)
		}

		return err
	}

	if *nowValue != "" && *legacyNowValue != "" {
		err := errors.New("-test-clock and -now cannot both be set")
		if command == "evaluate" {
			return evaluateFailure(cigate.EvaluationInput{}, err)
		}

		return err
	}

	clockValue := *nowValue
	if clockValue == "" {
		clockValue = *legacyNowValue
	}

	if clockValue != "" && os.Getenv("GRAITH_CIGATE_ALLOW_TEST_CLOCK") != "1" {
		err := errors.New("-test-clock requires GRAITH_CIGATE_ALLOW_TEST_CLOCK=1")
		if command == "evaluate" {
			return evaluateFailure(cigate.EvaluationInput{}, err)
		}

		return err
	}

	now, err := parseOptionalTime(clockValue)
	if err != nil {
		if command == "evaluate" {
			return evaluateFailure(cigate.EvaluationInput{}, err)
		}

		return err
	}

	if now.IsZero() {
		now = time.Now().UTC()
	}

	data, err := os.ReadFile(*inputPath)
	if err != nil {
		if command == "evaluate" {
			return writeFailedEvaluation(*outputPath, cigate.EvaluationInput{}, now, err)
		}

		return err
	}

	switch command {
	case "evaluate":
		if *replayStorePath == "" {
			return writeFailedEvaluation(*outputPath, cigate.EvaluationInput{}, now, errors.New("-replay-store is required with evaluate"))
		}

		if *trustedConfigPath == "" {
			return writeFailedEvaluation(*outputPath, cigate.EvaluationInput{}, now, errors.New("-trusted-config is required with evaluate"))
		}

		if *configDigest == "" {
			return writeFailedEvaluation(*outputPath, cigate.EvaluationInput{}, now, errors.New("-config-digest is required with evaluate"))
		}

		if *trustedPolicyPath == "" {
			return writeFailedEvaluation(*outputPath, cigate.EvaluationInput{}, now, errors.New("-trusted-policy is required with evaluate"))
		}

		if *policyDigest == "" {
			return writeFailedEvaluation(*outputPath, cigate.EvaluationInput{}, now, errors.New("-policy-digest is required with evaluate"))
		}

		input, err := cigate.DecodeEvaluationInput(*inputPath, data)
		if err != nil {
			return writeFailedEvaluation(*outputPath, cigate.EvaluationInput{}, now, err)
		}

		anchors, err := loadTrustAnchors(*trustedPolicyPath, *trustedConfigPath, *configDigest, *policyDigest, cigate.EvaluatorIdentity{
			Name:          cigate.CheckName,
			Version:       *evaluatorVersion,
			ReleaseDigest: *releaseDigest,
			SourceDigest:  *evaluatorDigest,
		})
		if err != nil {
			return writeFailedEvaluation(*outputPath, input, now, err)
		}

		store := cigate.NewFileReplayStore(*replayStorePath)

		evaluation, evalErr := cigate.EvaluateAt(input, anchors, store, now)

		output, err := cigate.EncodeCanonical(evaluation)
		if err != nil {
			return err
		}

		if err := writeOutput(*outputPath, output); err != nil {
			return err
		}

		return evalErr
	case "live-proof":
		if *replayStorePath != "" {
			return errors.New("-replay-store is only valid with evaluate")
		}

		if *trustedConfigPath == "" {
			return errors.New("-trusted-config is required with live-proof")
		}

		if *configDigest == "" {
			return errors.New("-config-digest is required with live-proof")
		}

		if *trustedPolicyPath != "" {
			return errors.New("-trusted-policy is only valid with evaluate")
		}

		if *policyDigest != "" {
			return errors.New("-policy-digest is only valid with evaluate")
		}

		if *evaluatorVersion != "" || *releaseDigest != "" || *evaluatorDigest != "" {
			return errors.New("-evaluator-version, -release-digest, and -evaluator-digest are only valid with evaluate")
		}

		bundle, err := cigate.DecodeLiveProofBundle(*inputPath, data)
		if err != nil {
			return err
		}

		config, err := loadTrustedConfig(*trustedConfigPath, *configDigest)
		if err != nil {
			return err
		}

		if err := cigate.ValidateLiveProofBundle(bundle, config, now); err != nil {
			return err
		}

		output, err := cigate.EncodeCanonical(bundle)
		if err != nil {
			return err
		}

		return writeOutput(*outputPath, output)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func loadTrustAnchors(policyPath, configPath, expectedConfigDigest, expectedPolicyDigest string, evaluator cigate.EvaluatorIdentity) (cigate.TrustAnchors, error) {
	policy, err := cipolicy.ReadManifest(policyPath)
	if err != nil {
		return cigate.TrustAnchors{}, err
	}

	config, err := loadTrustedConfig(configPath, expectedConfigDigest)
	if err != nil {
		return cigate.TrustAnchors{}, err
	}

	return cigate.TrustAnchors{
		Config:               config,
		Policy:               policy,
		ExpectedPolicyDigest: expectedPolicyDigest,
		Evaluator:            evaluator,
	}, nil
}

func writeFailedEvaluation(outputPath string, input cigate.EvaluationInput, now time.Time, evalErr error) error {
	evaluation := failedEvaluation(input, now, evalErr.Error())

	output, err := cigate.EncodeCanonical(evaluation)
	if err != nil {
		return errors.Join(evalErr, err)
	}

	if err := writeOutput(outputPath, output); err != nil {
		return errors.Join(evalErr, err)
	}

	return evalErr
}

func failedEvaluation(input cigate.EvaluationInput, now time.Time, reason string) cigate.Evaluation {
	return cigate.Evaluation{
		SchemaVersion: cigate.SchemaVersion,
		Check: cigate.CheckRun{
			Name:        cigate.CheckName,
			HeadSHA:     input.Event.IntendedSHA,
			Status:      "completed",
			Conclusion:  "failure",
			Title:       "CI evidence rejected",
			Summary:     reason,
			CompletedAt: now.UTC(),
		},
		Reasons: []string{reason},
	}
}

func loadTrustedConfig(path, expectedDigest string) (cigate.Config, error) {
	if err := validateExpectedDigest("config", expectedDigest); err != nil {
		return cigate.Config{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cigate.Config{}, err
	}

	if got := digestBytes(data); got != expectedDigest {
		return cigate.Config{}, fmt.Errorf("trusted config digest %s does not match expected config digest %s", got, expectedDigest)
	}

	config, err := cigate.DecodeConfig(path, data)
	if err != nil {
		return cigate.Config{}, err
	}

	return config, nil
}

func validateExpectedDigest(kind, digest string) error {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest {
		return fmt.Errorf("%s digest %q is invalid", kind, digest)
	}

	return nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeOutput(path string, data []byte) error {
	if path == "" {
		_, err := os.Stdout.Write(data)
		return err
	}

	return atomicfile.Write(path, data, 0o600)
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse now: %w", err)
	}

	return parsed, nil
}
