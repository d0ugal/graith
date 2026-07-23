package architecture

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func testManifest() Manifest {
	return Manifest{Version: 1, Module: "example.com/braw", Categories: []string{"composition", "provider"}, Packages: map[string]string{"example.com/braw/a": "composition", "example.com/braw/b": "provider"}, DefaultRemediation: "add an explicit allowed rule or a narrowly scoped expiring exception"}
}

func TestCheckAllowedAndForbiddenEdges(t *testing.T) {
	m := testManifest()
	m.Rules = []Rule{{From: "composition", To: "composition", Kind: "production", ID: "composition-contract"}}
	violations, err := Check(m, []Package{{ImportPath: "example.com/braw/a", Imports: []string{"example.com/braw/b", "example.com/braw/a"}}, {ImportPath: "example.com/braw/b"}}, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC))
	if err != nil || len(violations) != 1 || violations[0].Edge.To != "example.com/braw/b" {
		t.Fatalf("violations=%#v err=%v", violations, err)
	}
	if !strings.Contains(Format(violations[0]), "composition -> provider") {
		t.Fatalf("diagnostic=%s", Format(violations[0]))
	}
}

func TestCheckReportsTestEdgesSeparately(t *testing.T) {
	m := testManifest()
	m.Rules = nil
	violations, err := Check(m, []Package{{ImportPath: "example.com/braw/a", TestImports: []string{"example.com/braw/b"}, XTestImports: []string{"example.com/braw/b"}}, {ImportPath: "example.com/braw/b"}}, time.Now())
	if err != nil || len(violations) != 1 || violations[0].Edge.Kind != "test" {
		t.Fatalf("violations=%#v err=%v", violations, err)
	}
}

func TestLoadRejectsTrailingData(t *testing.T) {
	input := `{"version":1,"module":"example.com/braw","categories":["value"],"packages":{"example.com/braw/a":"value"},"default_remediation":"fix it"} {}`
	if _, err := Load(strings.NewReader(input)); !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckUnknownAndExpiredManifestData(t *testing.T) {
	m := testManifest()
	m.Exceptions = []Exception{{ID: "old", From: "example.com/braw/a", To: "example.com/braw/b", Kind: "production", Owner: "croft", Reason: "migration", Expires: "2026-07-22"}}
	if _, err := Check(m, []Package{{ImportPath: "example.com/braw/a"}}, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)); !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err=%v", err)
	}
	m.Exceptions[0].Expires = "2026-08-01"
	m.Exceptions[0].To = "example.com/braw/missing"
	if _, err := Check(m, []Package{{ImportPath: "example.com/braw/a"}}, time.Now()); !strings.Contains(err.Error(), "unknown imported") {
		t.Fatalf("err=%v", err)
	}
	m.Exceptions[0].To = "example.com/braw/b"
	m.Exceptions = append(m.Exceptions, Exception{ID: "new", From: "example.com/braw/a", To: "example.com/braw/b", Kind: "production", Owner: "bairn", Reason: "duplicate", Expires: "2026-08-02"})
	if _, err := Check(m, []Package{{ImportPath: "example.com/braw/a"}, {ImportPath: "example.com/braw/b"}}, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)); !strings.Contains(err.Error(), "duplicate exception edge") {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckPermittedLiveException(t *testing.T) {
	m := testManifest()
	m.Exceptions = []Exception{{ID: "production:example.com/braw/a->example.com/braw/b", From: "example.com/braw/a", To: "example.com/braw/b", Kind: "production", Owner: "croft", Reason: "migration", Expires: "2026-08-01"}}
	violations, err := Check(m, []Package{{ImportPath: "example.com/braw/a", Imports: []string{"example.com/braw/b"}}, {ImportPath: "example.com/braw/b"}}, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC))
	if err != nil || len(violations) != 1 || violations[0].Exception == "" {
		t.Fatalf("violations=%#v err=%v", violations, err)
	}
}

func TestCheckDoesNotApplyTestRuleToProduction(t *testing.T) {
	m := testManifest()
	m.Rules = []Rule{{From: "composition", To: "provider", Kind: "test", ID: "test-only"}}
	violations, err := Check(m, []Package{{ImportPath: "example.com/braw/a", Imports: []string{"example.com/braw/b"}, TestImports: []string{"example.com/braw/b"}}, {ImportPath: "example.com/braw/b"}}, time.Now())
	if err != nil || len(violations) != 1 || violations[0].Edge.Kind != "production" {
		t.Fatalf("violations=%#v err=%v", violations, err)
	}
}

func TestCheckRejectsMissingRuleKind(t *testing.T) {
	m := testManifest()
	m.Rules = []Rule{{From: "composition", To: "provider", ID: "missing-kind"}}
	if _, err := Check(m, []Package{{ImportPath: "example.com/braw/a"}, {ImportPath: "example.com/braw/b"}}, time.Now()); !strings.Contains(err.Error(), "malformed rule") {
		t.Fatalf("err=%v", err)
	}
}

func TestRealManifestCoversDiscoveredRepository(t *testing.T) {
	file, err := os.Open("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	packages, err := Discover("go", "../..")
	if err != nil {
		t.Fatal(err)
	}
	violations, err := Check(m, packages, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) < 40 || len(violations) != 0 {
		t.Fatalf("packages=%d violations=%#v", len(packages), violations)
	}
}

func TestRealManifestScopesTestOnlyRules(t *testing.T) {
	file, err := os.Open("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	packages, err := Discover("go", "../..")
	if err != nil {
		t.Fatal(err)
	}
	const config = "github.com/d0ugal/graith/internal/config"
	for i := range packages {
		if packages[i].ImportPath != config {
			continue
		}
		packages[i].Imports = append(packages[i].Imports, config)
		packages[i].TestImports = append(packages[i].TestImports, config)
		violations, checkErr := Check(m, packages, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC))
		if checkErr != nil {
			t.Fatal(checkErr)
		}
		if len(violations) != 1 || violations[0].Edge.From != config || violations[0].Edge.To != config || violations[0].Edge.Kind != "production" {
			t.Fatalf("violations=%#v", violations)
		}
		return
	}
	t.Fatalf("discovered package %s", config)
}

func TestDiscoveryVariantsCoverSupportedBuilds(t *testing.T) {
	variants := discoveryVariants()
	if len(variants) != 9 {
		t.Fatalf("variants=%#v", variants)
	}
	seen := make(map[string]bool)
	for _, variant := range variants {
		key := variant.goos + "/" + variant.goarch + ":" + strings.Join(variant.tags, ",") + ":" + fmt.Sprint(variant.cgo)
		seen[key] = true
	}
	if len(seen) != len(variants) {
		t.Fatalf("duplicate variants=%#v", variants)
	}
}

func TestDiscoveryIncludesTaggedOnlyEdges(t *testing.T) {
	packages, err := Discover("go", "../..")
	if err != nil {
		t.Fatal(err)
	}
	imports := make(map[string]map[string]bool)
	for _, p := range packages {
		imports[p.ImportPath] = map[string]bool{}
		for _, edge := range mergeStrings(p.Imports, mergeStrings(p.TestImports, p.XTestImports)) {
			imports[p.ImportPath][edge] = true
		}
	}
	if !imports["github.com/d0ugal/graith/internal/integration"]["github.com/d0ugal/graith/internal/pty"] {
		t.Fatal("releaseartifact integration edge was not discovered")
	}
	if !imports["github.com/d0ugal/graith/internal/pty"]["github.com/d0ugal/graith/internal/executablepin"] {
		t.Fatal("libghostty edge was not discovered")
	}
}
