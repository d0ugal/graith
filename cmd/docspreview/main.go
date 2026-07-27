package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/d0ugal/graith/internal/docspreview"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, time.Now, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, now func() time.Time, logOutput io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: docspreview publish|cleanup|prune [flags]")
	}

	switch args[0] {
	case "publish":
		return runPublish(args[1:], getenv, now, logOutput)
	case "cleanup":
		return runCleanup(args[1:], getenv, logOutput)
	case "prune":
		return runPrune(args[1:], getenv, now, logOutput)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runPublish(args []string, getenv func(string) string, now func() time.Time, logOutput io.Writer) error {
	flags := flag.NewFlagSet("docspreview publish", flag.ContinueOnError)
	common := addCommonFlags(flags, getenv)
	manifestPath := flags.String("manifest", "shots/out/manifest.json", "docs diff manifest path")
	assetsDir := flags.String("assets", "shots/out", "directory containing diff assets")
	sha := flags.String("sha", getenv("GITHUB_SHA"), "commit SHA used in the preview comment and storage path")
	runID := flags.String("run-id", getenv("GITHUB_RUN_ID"), "GitHub Actions run id")

	runAttempt := flags.String("run-attempt", getenv("GITHUB_RUN_ATTEMPT"), "GitHub Actions run attempt")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 0 {
		return errors.New("publish does not accept positional arguments")
	}

	event, err := readEvent(*common.eventPath)
	if err != nil {
		return err
	}

	repo, err := resolveRepository(*common.repository, event)
	if err != nil {
		return err
	}

	return docspreview.Publish(context.Background(), docspreview.PublishOptions{
		Client:       newGitHubClient(common),
		Logger:       streamLogger{output: logOutput},
		Repo:         repo,
		Event:        event,
		ManifestPath: *manifestPath,
		AssetsDir:    *assetsDir,
		SHA:          *sha,
		RunID:        *runID,
		RunAttempt:   *runAttempt,
		Now:          now(),
	})
}

func runCleanup(args []string, getenv func(string) string, logOutput io.Writer) error {
	flags := flag.NewFlagSet("docspreview cleanup", flag.ContinueOnError)

	common := addCommonFlags(flags, getenv)
	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 0 {
		return errors.New("cleanup does not accept positional arguments")
	}

	event, err := readEvent(*common.eventPath)
	if err != nil {
		return err
	}

	repo, err := resolveRepository(*common.repository, event)
	if err != nil {
		return err
	}

	return docspreview.Cleanup(context.Background(), docspreview.CleanupOptions{
		Client: newGitHubClient(common),
		Logger: streamLogger{output: logOutput},
		Repo:   repo,
		Event:  event,
	})
}

func runPrune(args []string, getenv func(string) string, now func() time.Time, logOutput io.Writer) error {
	flags := flag.NewFlagSet("docspreview prune", flag.ContinueOnError)

	common := addCommonFlags(flags, getenv)
	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 0 {
		return errors.New("prune does not accept positional arguments")
	}

	var event docspreview.Event

	if *common.eventPath != "" {
		loaded, err := readEvent(*common.eventPath)
		if err != nil {
			return err
		}

		event = loaded
	}

	repo, err := resolveRepository(*common.repository, event)
	if err != nil {
		return err
	}

	return docspreview.Prune(context.Background(), docspreview.PruneOptions{
		Client: newGitHubClient(common),
		Logger: streamLogger{output: logOutput},
		Repo:   repo,
		Now:    now(),
	})
}

type commonFlags struct {
	eventPath  *string
	repository *string
	token      *string
	apiURL     *string
}

func addCommonFlags(flags *flag.FlagSet, getenv func(string) string) commonFlags {
	return commonFlags{
		eventPath:  flags.String("event", getenv("GITHUB_EVENT_PATH"), "GitHub event JSON path"),
		repository: flags.String("repository", getenv("GITHUB_REPOSITORY"), "GitHub repository owner/name"),
		token:      flags.String("token", getenv("GITHUB_TOKEN"), "GitHub token"),
		apiURL:     flags.String("api-url", "https://api.github.com", "GitHub API base URL"),
	}
}

func newGitHubClient(flags commonFlags) *docspreview.GitHubHTTPClient {
	return &docspreview.GitHubHTTPClient{
		Client:  &http.Client{Timeout: 30 * time.Second},
		BaseURL: *flags.apiURL,
		Token:   *flags.token,
	}
}

func readEvent(path string) (docspreview.Event, error) {
	if path == "" {
		return docspreview.Event{}, errors.New("-event or GITHUB_EVENT_PATH is required")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return docspreview.Event{}, err
	}

	var event docspreview.Event
	if err := json.Unmarshal(data, &event); err != nil {
		return docspreview.Event{}, err
	}

	return event, nil
}

func resolveRepository(value string, event docspreview.Event) (docspreview.Repository, error) {
	if value != "" {
		return docspreview.ParseRepository(value)
	}

	return docspreview.RepositoryFromEvent(event)
}

type streamLogger struct {
	output io.Writer
}

func (logger streamLogger) Info(message string) {
	if logger.output == nil {
		return
	}

	_, _ = fmt.Fprintln(logger.output, message)
}
