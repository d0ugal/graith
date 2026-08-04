package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/d0ugal/graith/internal/ciworkflow"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("ciclassify", flag.ContinueOnError)
	mode := flags.String("mode", "", "classifier mode: ci, coverage, sandbox, libghostty, dev-release, stable-release, docs-preview, session-navigator-preview")
	changedFilesPath := flags.String("changed-files", "", "newline-delimited changed file list; stdin when empty")
	githubOutput := flags.String("github-output", "", "append GitHub Actions outputs to this file")
	jsonOutput := flags.Bool("json", false, "write full classifier result as JSON")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}

	if *mode == "" && !*jsonOutput {
		return errors.New("ciclassify requires -mode unless -json is set")
	}

	files, err := readChangedFiles(*changedFilesPath, stdin)
	if err != nil {
		return err
	}

	result, err := ciworkflow.ClassifyWorkflowPaths(files)
	if err != nil {
		return err
	}

	var outputs map[string]bool
	if *mode != "" {
		outputs, err = ciworkflow.WorkflowModeOutputs(ciworkflow.WorkflowClassifierMode(*mode), result)
		if err != nil {
			return err
		}
	}

	if *githubOutput != "" {
		if len(outputs) == 0 {
			return errors.New("-github-output requires -mode")
		}

		if err := appendGitHubOutputs(*githubOutput, outputs); err != nil {
			return err
		}
	}

	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(result); err != nil {
			return err
		}

		return nil
	}

	if *githubOutput != "" {
		return nil
	}

	return writeShellOutputs(stdout, outputs)
}

func readChangedFiles(path string, stdin io.Reader) ([]string, error) {
	if path == "" {
		return scanChangedFiles("stdin", stdin)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = file.Close()
	}()

	return scanChangedFiles(path, file)
}

func scanChangedFiles(name string, reader io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(reader)

	var files []string
	for scanner.Scan() {
		files = append(files, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read changed files from %s: %w", name, err)
	}

	return files, nil
}

func appendGitHubOutputs(path string, outputs map[string]bool) error {
	var builder strings.Builder
	for _, key := range sortedOutputKeys(outputs) {
		fmt.Fprintf(&builder, "%s=%t\n", key, outputs[key])
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	if _, err := file.WriteString(builder.String()); err != nil {
		_ = file.Close()
		return err
	}

	return file.Close()
}

func writeShellOutputs(writer io.Writer, outputs map[string]bool) error {
	for _, key := range sortedOutputKeys(outputs) {
		if _, err := fmt.Fprintf(writer, "%s=%t\n", key, outputs[key]); err != nil {
			return err
		}
	}

	return nil
}

func sortedOutputKeys(outputs map[string]bool) []string {
	keys := make([]string, 0, len(outputs))
	for key := range outputs {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
