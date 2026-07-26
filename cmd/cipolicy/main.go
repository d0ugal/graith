package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/atomicfile"
	"github.com/d0ugal/graith/internal/cipolicy"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("cipolicy", flag.ContinueOnError)
	inventoryPath := flags.String("inventory", cipolicy.DefaultInventoryPath, "P0 baseline inventory path")
	manifestPath := flags.String("manifest", cipolicy.DefaultManifestPath, "policy manifest path")
	outputPath := flags.String("output", "", "output path for generate (stdout when empty)")
	changedFilesPath := flags.String("changed-files", "", "newline-delimited changed file list for plan replay")
	planInputPath := flags.String("plan-input", "", "run plan JSON path for summary")
	planErrorPath := flags.String("plan-error", "", "run plan error path for summary")
	headSHA := flags.String("head-sha", "", "reported source head SHA for summary")
	runURL := flags.String("run-url", "", "Actions run URL for summary")
	macOSDetectorResult := flags.String("macos-detector-result", "", "macOS detector job result for summary")
	macOSDetectorOutput := flags.String("macos-detector-output", "", "macOS detector output value for summary")
	eventName := flags.String("event", "", "GitHub event name for plan replay")
	ref := flags.String("ref", "", "Git ref for plan replay")
	baseRef := flags.String("base-ref", "", "base ref for plan replay")
	headRef := flags.String("head-ref", "", "head ref for plan replay")
	baseRepository := flags.String("base-repository", cipolicy.DefaultRepository, "base repository for plan replay")
	headRepository := flags.String("head-repository", "", "head repository for plan replay")
	commit := flags.String("commit", "", "source commit digest for plan replay")
	tree := flags.String("tree", "", "source tree digest for plan replay")
	pullRequestFork := flags.Bool("fork", false, "classify a pull_request as fork-untrusted")
	sameRepositoryAgent := flags.Bool("same-repository-agent", false, "classify a pull_request as same-repository-agent")
	trustedBase := flags.Bool("trusted-base", false, "classify a pull_request as trusted-base")
	publication := flags.Bool("publication", false, "classify a push as trusted-publication")
	createdAt := flags.String("created-at", "", "RFC3339 plan creation time")
	expiresAt := flags.String("expires-at", "", "RFC3339 plan expiry time")

	var detectorErrors stringListFlag
	flags.Var(&detectorErrors, "detector-error", "detector error to bind into a safe-superset plan; may be repeated")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 1 {
		return errors.New("usage: cipolicy [flags] generate|validate|digest|plan|summary")
	}

	command := flags.Arg(0)
	if command != "generate" && command != "plan" && *outputPath != "" {
		return errors.New("-output is only valid with generate or plan")
	}

	switch command {
	case "generate":
		manifest, err := cipolicy.GenerateFromInventoryPath(*inventoryPath)
		if err != nil {
			return err
		}

		data, err := manifest.MarshalCanonical()
		if err != nil {
			return err
		}

		generated, err := cipolicy.DecodeManifest("generated policy manifest", data)
		if err != nil {
			return err
		}

		if err := generated.Validate(); err != nil {
			return err
		}

		return writeOutput(*outputPath, data)
	case "validate":
		manifest, err := cipolicy.ReadManifest(*manifestPath)
		if err != nil {
			return err
		}

		if err := cipolicy.ValidateCurrent(manifest, *inventoryPath); err != nil {
			return err
		}

		fmt.Printf("%s\n", manifest.PolicyDigest)

		return nil
	case "digest":
		manifest, err := cipolicy.ReadManifest(*manifestPath)
		if err != nil {
			return err
		}

		if err := manifest.Validate(); err != nil {
			return err
		}

		fmt.Printf("%s\n", manifest.PolicyDigest)

		return nil
	case "plan":
		if *eventName == "" || *commit == "" || *tree == "" {
			return errors.New("plan requires -event, -commit, and -tree")
		}

		manifest, err := cipolicy.ReadManifest(*manifestPath)
		if err != nil {
			return err
		}

		changedFiles, exactFileList, err := readChangedFiles(*changedFilesPath)
		if err != nil {
			return err
		}

		created, err := parseOptionalTime(*createdAt, "created-at")
		if err != nil {
			return err
		}

		expires, err := parseOptionalTime(*expiresAt, "expires-at")
		if err != nil {
			return err
		}

		plan, err := cipolicy.BuildPlan(manifest, cipolicy.PlanOptions{
			Event: cipolicy.EventInput{
				GitHubEvent:         *eventName,
				Ref:                 *ref,
				BaseRef:             *baseRef,
				HeadRef:             *headRef,
				BaseRepository:      *baseRepository,
				HeadRepository:      *headRepository,
				Commit:              *commit,
				Tree:                *tree,
				PullRequestFork:     *pullRequestFork,
				SameRepositoryAgent: *sameRepositoryAgent,
				TrustedBase:         *trustedBase,
				Publication:         *publication,
			},
			ChangedFiles:   changedFiles,
			ExactFileList:  exactFileList,
			DetectorErrors: nonEmptyStrings([]string(detectorErrors)),
			CreatedAt:      created,
			ExpiresAt:      expires,
		})
		if err != nil {
			return err
		}

		data, err := plan.MarshalCanonical()
		if err != nil {
			return err
		}

		return writeOutput(*outputPath, data)
	case "summary":
		inventory, err := cipolicy.ReadInventory(*inventoryPath)
		if err != nil {
			return err
		}

		plan, err := readOptionalRunPlan(*planInputPath)
		if err != nil {
			return err
		}

		if plan != nil {
			manifest, err := cipolicy.ReadManifest(*manifestPath)
			if err != nil {
				return err
			}

			if err := plan.Validate(manifest); err != nil {
				return fmt.Errorf("validate summary plan: %w", err)
			}
		}

		changedFiles, _, err := readChangedFiles(*changedFilesPath)
		if err != nil {
			return err
		}

		summary, err := cipolicy.RenderShadowSummary(cipolicy.ShadowSummaryInput{
			Inventory:           inventory,
			Plan:                plan,
			PlanError:           readOptionalFile(*planErrorPath),
			ChangedFiles:        nonEmptyStrings(changedFiles),
			EventName:           *eventName,
			Ref:                 *ref,
			HeadSHA:             *headSHA,
			RunURL:              *runURL,
			MacOSDetectorResult: *macOSDetectorResult,
			MacOSDetectorOutput: *macOSDetectorOutput,
		})
		if err != nil {
			return err
		}

		_, err = os.Stdout.WriteString(summary)

		return err
	default:
		return fmt.Errorf("unknown command %q", flags.Arg(0))
	}
}

func writeOutput(path string, data []byte) error {
	if path == "" {
		_, err := os.Stdout.Write(data)
		return err
	}

	return atomicfile.Write(path, data, 0o600)
}

func readChangedFiles(path string) (files []string, exact bool, err error) {
	if path == "" {
		return nil, false, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}

	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		files = append(files, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, false, err
	}

	return files, true, nil
}

func readOptionalRunPlan(path string) (*cipolicy.RunPlan, error) {
	if path == "" {
		return nil, nil
	}

	loaded, err := cipolicy.ReadRunPlan(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return &loaded, nil
}

func readOptionalFile(path string) string {
	if path == "" {
		return ""
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return string(data)
}

type stringListFlag []string

func (flag *stringListFlag) String() string {
	if flag == nil {
		return ""
	}

	return strings.Join(*flag, "\n")
}

func (flag *stringListFlag) Set(value string) error {
	*flag = append(*flag, value)

	return nil
}

func nonEmptyStrings(values []string) []string {
	filtered := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}

		filtered = append(filtered, value)
	}

	return filtered
}

func parseOptionalTime(value, name string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s: %w", name, err)
	}

	return parsed, nil
}
