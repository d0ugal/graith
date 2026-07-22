// Command packagegraph generates and verifies the committed Hugo data used by
// the contributing package-dependencies page.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	graphGOOS              = "linux"
	graphGOARCH            = "amd64"
	regenerateGraphCommand = "make package-graph"
)

var errGraphStale = errors.New("package dependency graph is stale")

type listedPackage struct {
	ImportPath string
	Imports    []string
}

type graphData struct {
	Module    string      `json:"module"`
	Entry     string      `json:"entry"`
	Platform  string      `json:"platform"`
	Packages  []graphNode `json:"packages"`
	Relations []graphEdge `json:"relations"`
}

type graphNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Group string `json:"group"`
}

type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "packagegraph: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("packagegraph", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", "..", "path to the graith repository root")
	output := flags.String("output", "data/package_dependencies.json", "Hugo data file to write")
	check := flags.Bool("check", false, "check that the output matches the current package graph")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	graph, err := inspectRepository(ctx, *repo)
	if err != nil {
		return err
	}
	if *check {
		if err := checkGraph(*output, graph); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s is up to date (%d packages, %d relationships)\n", *output, len(graph.Packages), len(graph.Relations))
		return err
	}
	if err := writeGraph(*output, graph); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "generated %s (%d packages, %d relationships)\n", *output, len(graph.Packages), len(graph.Relations))
	return err
}

func inspectRepository(ctx context.Context, repo string) (graphData, error) {
	moduleBytes, err := runGo(ctx, repo, "list", "-m", "-f", "{{.Path}}")
	if err != nil {
		return graphData{}, fmt.Errorf("find module path: %w", err)
	}
	module := strings.TrimSpace(string(moduleBytes))
	if module == "" {
		return graphData{}, errors.New("go list returned an empty module path")
	}

	packageBytes, err := runGo(ctx, repo, "list", "-deps", "-json", "./cmd/graith")
	if err != nil {
		return graphData{}, fmt.Errorf("list packages: %w", err)
	}
	packages, err := decodePackages(bytes.NewReader(packageBytes))
	if err != nil {
		return graphData{}, fmt.Errorf("decode package list: %w", err)
	}
	return buildGraph(module, module+"/cmd/graith", packages)
}

func runGo(ctx context.Context, repo string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = repo
	command.Env = canonicalGoEnv(os.Environ())
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return nil, fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, message)
		}
		return nil, fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func canonicalGoEnv(environ []string) []string {
	result := make([]string, 0, len(environ)+3)
	for _, value := range environ {
		if strings.HasPrefix(value, "GOOS=") || strings.HasPrefix(value, "GOARCH=") || strings.HasPrefix(value, "CGO_ENABLED=") {
			continue
		}
		result = append(result, value)
	}
	return append(result, "GOOS="+graphGOOS, "GOARCH="+graphGOARCH, "CGO_ENABLED=0")
}

func decodePackages(reader io.Reader) ([]listedPackage, error) {
	decoder := json.NewDecoder(reader)
	var packages []listedPackage
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				return packages, nil
			}
			return nil, err
		}
		packages = append(packages, pkg)
	}
}

func buildGraph(module, entry string, listed []listedPackage) (graphData, error) {
	prefix := module + "/"
	packagesByPath := make(map[string]listedPackage)
	for _, pkg := range listed {
		if pkg.ImportPath != entry && !strings.HasPrefix(pkg.ImportPath, module+"/internal/") {
			continue
		}
		if _, exists := packagesByPath[pkg.ImportPath]; exists {
			return graphData{}, fmt.Errorf("duplicate package %q", pkg.ImportPath)
		}
		packagesByPath[pkg.ImportPath] = pkg
	}
	if _, ok := packagesByPath[entry]; !ok {
		return graphData{}, fmt.Errorf("entry package %q was not listed", entry)
	}

	paths := make([]string, 0, len(packagesByPath))
	for path := range packagesByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	graph := graphData{
		Module:   module,
		Entry:    strings.TrimPrefix(entry, prefix),
		Platform: graphGOOS + "/" + graphGOARCH,
		Packages: make([]graphNode, 0, len(paths)),
	}
	ids := make(map[string]string, len(paths))
	for index, path := range paths {
		id := fmt.Sprintf("package_%d", index)
		ids[path] = id
		group := "internal"
		if path == entry {
			group = "entry"
		}
		graph.Packages = append(graph.Packages, graphNode{
			ID:    id,
			Label: strings.TrimPrefix(path, prefix),
			Group: group,
		})
	}

	type pathEdge struct {
		from string
		to   string
	}
	var edges []pathEdge
	for _, from := range paths {
		for _, to := range packagesByPath[from].Imports {
			if _, ok := packagesByPath[to]; ok {
				edges = append(edges, pathEdge{from: from, to: to})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from == edges[j].from {
			return edges[i].to < edges[j].to
		}
		return edges[i].from < edges[j].from
	})
	graph.Relations = make([]graphEdge, 0, len(edges))
	for _, edge := range edges {
		graph.Relations = append(graph.Relations, graphEdge{From: ids[edge.from], To: ids[edge.to]})
	}
	return graph, nil
}

func writeGraph(path string, graph graphData) error {
	data, err := encodeGraph(graph)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".package_dependencies-*.json")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary output permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace output: %w", err)
	}
	return nil
}

func checkGraph(path string, graph graphData) error {
	want, err := encodeGraph(graph)
	if err != nil {
		return err
	}
	got, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read committed graph: %w", err)
		}
		return staleGraphError(path)
	}
	if !bytes.Equal(got, want) {
		return staleGraphError(path)
	}
	return nil
}

func staleGraphError(path string) error {
	return fmt.Errorf("%w: %s does not match the current source; run `%s` from the repository root and commit the result", errGraphStale, path, regenerateGraphCommand)
}

func encodeGraph(graph graphData) ([]byte, error) {
	data, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode graph: %w", err)
	}
	return append(data, '\n'), nil
}
