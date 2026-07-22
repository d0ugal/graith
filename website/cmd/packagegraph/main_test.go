package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildGraph(t *testing.T) {
	listed := []listedPackage{
		{ImportPath: "example.com/braw/internal/strath"},
		{
			ImportPath: "example.com/braw/cmd/croft",
			Imports:    []string{"example.com/braw/internal/bothy", "net/http"},
		},
		{
			ImportPath: "example.com/braw/internal/bothy",
			Imports:    []string{"example.com/braw/internal/strath"},
		},
		{ImportPath: "net/http", Imports: []string{"context"}},
	}

	got, err := buildGraph("example.com/braw", "example.com/braw/cmd/croft", listed)
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	want := graphData{
		Module:   "example.com/braw",
		Entry:    "cmd/croft",
		Platform: "linux/amd64",
		Packages: []graphNode{
			{ID: "package_0", Label: "cmd/croft", Group: "entry"},
			{ID: "package_1", Label: "internal/bothy", Group: "internal"},
			{ID: "package_2", Label: "internal/strath", Group: "internal"},
		},
		Relations: []graphEdge{
			{From: "package_0", To: "package_1"},
			{From: "package_1", To: "package_2"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("graph mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestBuildGraphDeterministic(t *testing.T) {
	first, err := buildGraph("example.com/braw", "example.com/braw/cmd/croft", []listedPackage{
		{ImportPath: "example.com/braw/internal/strath"},
		{
			ImportPath: "example.com/braw/cmd/croft",
			Imports:    []string{"example.com/braw/internal/strath", "example.com/braw/internal/bothy"},
		},
		{ImportPath: "example.com/braw/internal/bothy"},
	})
	if err != nil {
		t.Fatalf("build first graph: %v", err)
	}
	second, err := buildGraph("example.com/braw", "example.com/braw/cmd/croft", []listedPackage{
		{ImportPath: "example.com/braw/internal/bothy"},
		{
			ImportPath: "example.com/braw/cmd/croft",
			Imports:    []string{"example.com/braw/internal/bothy", "example.com/braw/internal/strath"},
		},
		{ImportPath: "example.com/braw/internal/strath"},
	})
	if err != nil {
		t.Fatalf("build second graph: %v", err)
	}
	firstJSON, err := encodeGraph(first)
	if err != nil {
		t.Fatalf("encode first graph: %v", err)
	}
	secondJSON, err := encodeGraph(second)
	if err != nil {
		t.Fatalf("encode second graph: %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("graph output changed with input ordering\nfirst:\n%s\nsecond:\n%s", firstJSON, secondJSON)
	}
}

func TestBuildGraphRequiresEntryPackage(t *testing.T) {
	_, err := buildGraph("example.com/braw", "example.com/braw/cmd/croft", []listedPackage{
		{ImportPath: "example.com/braw/internal/bothy"},
	})
	if err == nil || !strings.Contains(err.Error(), "entry package") {
		t.Fatalf("error = %v, want missing entry package error", err)
	}
}

func TestBuildGraphRejectsDuplicatePackage(t *testing.T) {
	_, err := buildGraph("example.com/braw", "example.com/braw/cmd/croft", []listedPackage{
		{ImportPath: "example.com/braw/cmd/croft"},
		{ImportPath: "example.com/braw/cmd/croft"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate package") {
		t.Fatalf("error = %v, want duplicate package error", err)
	}
}

func TestDecodePackages(t *testing.T) {
	input := strings.NewReader(`{"ImportPath":"example.com/braw/internal/bothy","Imports":["fmt"]}
{"ImportPath":"example.com/braw/internal/strath"}`)
	got, err := decodePackages(input)
	if err != nil {
		t.Fatalf("decodePackages: %v", err)
	}
	want := []listedPackage{
		{ImportPath: "example.com/braw/internal/bothy", Imports: []string{"fmt"}},
		{ImportPath: "example.com/braw/internal/strath"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packages = %#v, want %#v", got, want)
	}
}

func TestWriteGraph(t *testing.T) {
	output := filepath.Join(t.TempDir(), "nested", "package_dependencies.json")
	want := graphData{
		Module:   "example.com/braw",
		Entry:    "cmd/croft",
		Platform: "linux/amd64",
		Packages: []graphNode{{ID: "package_0", Label: "cmd/croft", Group: "entry"}},
	}
	if err := writeGraph(output, want); err != nil {
		t.Fatalf("writeGraph: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if data[len(data)-1] != '\n' {
		t.Fatal("output does not end with a newline")
	}
	var got graphData
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("graph = %#v, want %#v", got, want)
	}
}

func TestCheckGraph(t *testing.T) {
	graph := graphData{
		Module:   "example.com/braw",
		Entry:    "cmd/croft",
		Platform: "linux/amd64",
		Packages: []graphNode{{ID: "package_0", Label: "cmd/croft", Group: "entry"}},
	}

	t.Run("current", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "package_dependencies.json")
		if err := writeGraph(output, graph); err != nil {
			t.Fatalf("writeGraph: %v", err)
		}
		if err := checkGraph(output, graph); err != nil {
			t.Fatalf("checkGraph: %v", err)
		}
	})

	t.Run("stale", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "package_dependencies.json")
		stale := []byte("{\"module\":\"example.com/dreich\"}\n")
		if err := os.WriteFile(output, stale, 0o644); err != nil {
			t.Fatalf("write stale fixture: %v", err)
		}

		err := checkGraph(output, graph)
		if !errors.Is(err, errGraphStale) {
			t.Fatalf("error = %v, want errGraphStale", err)
		}
		if !strings.Contains(err.Error(), regenerateGraphCommand) {
			t.Fatalf("error = %q, want regeneration command", err)
		}
		got, readErr := os.ReadFile(output)
		if readErr != nil {
			t.Fatalf("read stale fixture: %v", readErr)
		}
		if !bytes.Equal(got, stale) {
			t.Fatalf("stale check modified output\n got: %q\nwant: %q", got, stale)
		}
	})
}

func TestRunRejectsUnexpectedArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"blether"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("error = %v, want unexpected arguments error", err)
	}
}

func TestCanonicalGoEnvReplacesTarget(t *testing.T) {
	got := canonicalGoEnv([]string{
		"PATH=/bin",
		"GOOS=darwin",
		"GOARCH=arm64",
		"CGO_ENABLED=1",
		"GOFLAGS=-tags=thrawn -mod=mod",
		"GOWORK=/tmp/dreich.work",
	})
	want := []string{
		"PATH=/bin",
		"GOOS=linux",
		"GOARCH=amd64",
		"CGO_ENABLED=0",
		"GOFLAGS=-mod=readonly",
		"GOWORK=off",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}
