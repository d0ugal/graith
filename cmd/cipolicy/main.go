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
	manifestPath := flags.String("manifest", cipolicy.DefaultManifestPath, "policy manifest path")
	outputPath := flags.String("output", "", "output path for plan (stdout when empty)")
	changedFilesPath := flags.String("changed-files", "", "newline-delimited changed file list for plan replay")
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
		return errors.New("usage: cipolicy [flags] plan")
	}

	command := flags.Arg(0)
	if command != "plan" {
		return fmt.Errorf("unknown command %q", flags.Arg(0))
	}

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
