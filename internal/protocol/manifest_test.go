package protocol

import (
	"bytes"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// update, when set, rewrites the committed manifest fixture from the current
// messages.go. Run:
//
//	go test ./internal/protocol -run TestManifestUpToDate -update
//
// after changing any wire struct, then commit the regenerated fixture.
var update = flag.Bool("update", false, "regenerate the committed protocol manifest fixture")

// manifestFixturePath is the single committed copy of the manifest. It lives in
// the Swift test target so SwiftPM can bundle it as a resource; the Go test
// reaches it by relative path from this package (internal/protocol). Both sides
// read the same bytes — there is no second copy to drift.
//
// A happy side effect of the fixture living under gui/: regenerating it from a
// Go change touches a gui/ file, which trips the paths-filtered gui/ Swift CI —
// so a Go PR that adds a Swift-required message can't merge green while
// Messages.swift is behind.
var manifestFixturePath = filepath.Join(
	"..", "..", "gui", "shared", "Tests", "GraithProtocolTests", "Fixtures", "protocol_manifest.json",
)

// TestManifestUpToDate regenerates the manifest and diffs it against the
// committed fixture. It runs on every PR (the Go suite has no paths filter), so
// a change to messages.go that isn't regenerated fails here — closing the gap
// that gui/ Swift CI would miss a Go-only change.
func TestManifestUpToDate(t *testing.T) {
	m, err := BuildManifest()
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}

	got, err := MarshalManifest(m)
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}

	if *update {
		if err := os.MkdirAll(filepath.Dir(manifestFixturePath), 0o750); err != nil {
			t.Fatalf("mkdir fixture dir: %v", err)
		}

		if err := os.WriteFile(manifestFixturePath, got, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}

		t.Logf("wrote %s", manifestFixturePath)

		return
	}

	want, err := os.ReadFile(manifestFixturePath)
	if err != nil {
		t.Fatalf("read committed fixture (%s): %v\nregenerate with: go test ./internal/protocol -run TestManifestUpToDate -update", manifestFixturePath, err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("protocol manifest is stale vs messages.go.\n"+
			"Regenerate and commit it:\n"+
			"  go test ./internal/protocol -run TestManifestUpToDate -update\n"+
			"Fixture: %s", manifestFixturePath)
	}
}

// TestManifestRegistryComplete parses messages.go and fails if any exported
// struct is missing from registeredTypes (or a stale entry lingers), and if any
// registered type lacks a swiftAnnotations classification. This is the same
// fail-closed discipline as TestRemoteMatrixCompleteness: a new wire struct
// forces a deliberate manifest + Swift-expectation decision in the same change.
func TestManifestRegistryComplete(t *testing.T) {
	declared := exportedStructsInMessagesGo(t)

	registered := map[string]bool{}
	for _, v := range registeredTypes {
		registered[structName(v)] = true
	}

	for name := range declared {
		if !registered[name] {
			t.Errorf("struct %q is declared in messages.go but missing from registeredTypes in manifest.go — add it (and a swiftAnnotations entry)", name)
		}
	}

	for name := range registered {
		if !declared[name] {
			t.Errorf("registeredTypes has %q with no matching struct in messages.go — stale entry", name)
		}

		if _, ok := swiftAnnotations[name]; !ok {
			t.Errorf("registered type %q has no swiftAnnotations entry — classify it (required/planned/na)", name)
		}
	}

	for name := range swiftAnnotations {
		if !registered[name] {
			t.Errorf("swiftAnnotations has entry %q with no registered type — stale entry", name)
		}
	}
}

// TestBuildManifestSucceeds is a fast guard that generation itself is healthy
// (every field maps to a known kind, every type is classified). TestManifestUpToDate
// depends on it, but this keeps a focused failure when a field type is added
// that describeType can't handle.
func TestBuildManifestSucceeds(t *testing.T) {
	m, err := BuildManifest()
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}

	if m.ProtocolVersion != Version {
		t.Errorf("manifest ProtocolVersion = %q, want %q", m.ProtocolVersion, Version)
	}

	if len(m.Types) != len(registeredTypes) {
		t.Errorf("manifest has %d types, registeredTypes has %d", len(m.Types), len(registeredTypes))
	}

	var required, planned, na int

	for _, mt := range m.Types {
		switch mt.Swift {
		case SwiftRequired:
			required++

			if mt.SwiftType == "" {
				t.Errorf("type %q is required but has no swift_type", mt.Name)
			}
		case SwiftPlanned:
			planned++
		case SwiftNA:
			na++
		default:
			t.Errorf("type %q has unknown swift expectation %q", mt.Name, mt.Swift)
		}
	}

	t.Logf("protocol manifest: %d types (%d required, %d planned, %d n/a)", len(m.Types), required, planned, na)
}

// exportedStructsInMessagesGo returns the set of exported struct type names
// declared in messages.go (the wire-protocol source of truth).
func exportedStructsInMessagesGo(t *testing.T) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, "messages.go", nil, 0)
	if err != nil {
		t.Fatalf("parse messages.go: %v", err)
	}

	structs := map[string]bool{}

	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}

		if _, ok := ts.Type.(*ast.StructType); !ok {
			return true
		}

		if !ts.Name.IsExported() {
			return true
		}

		structs[ts.Name.Name] = true

		return true
	})

	if len(structs) < 100 {
		t.Fatalf("only found %d exported structs in messages.go — parser likely broken", len(structs))
	}

	return structs
}

// structName returns the type name of a struct value.
func structName(v any) string {
	return reflect.TypeOf(v).Name()
}
