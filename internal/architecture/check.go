// Package architecture checks the repository's component dependency contract.
package architecture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type Manifest struct {
	Version            int               `json:"version"`
	Module             string            `json:"module"`
	Categories         []string          `json:"categories"`
	Packages           map[string]string `json:"packages"`
	Rules              []Rule            `json:"rules"`
	Exceptions         []Exception       `json:"exceptions"`
	DefaultRemediation string            `json:"default_remediation"`
}

type Rule struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type Exception struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Kind    string `json:"kind"`
	Owner   string `json:"owner"`
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
}

type Package struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

type Edge struct{ From, To, Kind string }
type Violation struct {
	Edge                         Edge
	Rule, Remediation, Exception string
}

func Load(r io.Reader) (Manifest, error) {
	var m Manifest
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Version != 1 || strings.TrimSpace(m.Module) == "" {
		return m, errors.New("manifest requires version 1 and module")
	}
	if len(m.Packages) == 0 {
		return m, errors.New("manifest has no packages")
	}
	if len(m.Categories) == 0 {
		return m, errors.New("manifest has no categories")
	}
	if strings.TrimSpace(m.DefaultRemediation) == "" {
		return m, errors.New("manifest has no default remediation")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return m, errors.New("manifest contains trailing data")
	}
	return m, nil
}

func Discover(goBinary, dir string) ([]Package, error) {
	merged := make(map[string]Package)
	for _, variant := range discoveryVariants() {
		packages, err := discoverPlatform(goBinary, dir, variant)
		if err != nil {
			return nil, err
		}
		for _, p := range packages {
			current := merged[p.ImportPath]
			current.ImportPath = p.ImportPath
			current.Imports = mergeStrings(current.Imports, p.Imports)
			current.TestImports = mergeStrings(current.TestImports, p.TestImports)
			current.XTestImports = mergeStrings(current.XTestImports, p.XTestImports)
			merged[p.ImportPath] = current
		}
	}
	result := make([]Package, 0, len(merged))
	for _, p := range merged {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ImportPath < result[j].ImportPath })
	return result, nil
}

type discoveryVariant struct {
	goos, goarch string
	cgo          bool
	tags         []string
}

func discoveryVariants() []discoveryVariant {
	variants := make([]discoveryVariant, 0, 18)
	for _, platform := range []struct{ goos, goarch string }{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}} {
		variants = append(variants,
			discoveryVariant{goos: platform.goos, goarch: platform.goarch},
		)
		variants = append(variants,
			discoveryVariant{goos: platform.goos, goarch: platform.goarch, tags: []string{"integration"}},
			discoveryVariant{goos: platform.goos, goarch: platform.goarch, cgo: true, tags: []string{"integration", "releaseartifact"}},
			discoveryVariant{goos: platform.goos, goarch: platform.goarch, cgo: true, tags: []string{"libghostty"}},
			discoveryVariant{goos: platform.goos, goarch: platform.goarch, tags: []string{"safehouse_enforce"}},
		)
		if platform.goos == "linux" {
			variants = append(variants, discoveryVariant{goos: platform.goos, goarch: platform.goarch, tags: []string{"nono_enforce"}})
		}
	}
	return variants
}

func discoverPlatform(goBinary, dir string, variant discoveryVariant) ([]Package, error) {
	tags := strings.Join(variant.tags, " ")
	args := []string{"list"}
	if tags != "" {
		args = append(args, "-tags="+tags)
	}
	args = append(args, "-json", "./...")
	cmd := exec.Command(goBinary, args...)
	cmd.Dir = dir
	cmd.Env = canonicalEnv(variant.goos, variant.goarch, variant.cgo)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list packages for %s/%s tags=%s: %w: %s", variant.goos, variant.goarch, tags, err, strings.TrimSpace(stderr.String()))
	}
	var result []Package
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var p Package
		if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list for %s/%s tags=%s: %w", variant.goos, variant.goarch, tags, err)
		}
		result = append(result, p)
	}
	return result, nil
}

func mergeStrings(left, right []string) []string {
	seen := make(map[string]bool, len(left)+len(right))
	for _, value := range left {
		seen[value] = true
	}
	for _, value := range right {
		seen[value] = true
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func canonicalEnv(goos, goarch string, cgo bool) []string {
	// go list must not inherit caller-selected tags, workspace, or platform.
	result := make([]string, 0, len(os.Environ())+5)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "GOFLAGS=") || strings.HasPrefix(value, "GOWORK=") || strings.HasPrefix(value, "GOOS=") || strings.HasPrefix(value, "GOARCH=") || strings.HasPrefix(value, "CGO_ENABLED=") {
			continue
		}
		result = append(result, value)
	}
	cgoEnabled := "0"
	if cgo {
		cgoEnabled = "1"
	}
	return append(result, "GOFLAGS=-mod=readonly", "GOWORK=off", "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED="+cgoEnabled)
}

func Check(m Manifest, packages []Package, now time.Time) ([]Violation, error) {
	if err := validateManifest(m, now); err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(packages))
	for _, p := range packages {
		known[p.ImportPath] = true
	}
	for _, path := range sortedKeys(known) {
		if _, ok := m.Packages[path]; !ok {
			return nil, fmt.Errorf("unclassified package %s: add it to architecture manifest", path)
		}
	}
	manifestPaths := make(map[string]bool, len(m.Packages))
	for path := range m.Packages {
		manifestPaths[path] = true
	}
	for _, path := range sortedKeys(manifestPaths) {
		if !known[path] {
			return nil, fmt.Errorf("manifest package %s was not returned by go list", path)
		}
	}
	allowed := func(from, to, kind string) (Rule, bool) {
		for _, r := range m.Rules {
			if (r.From == from || r.From == "*") && (r.To == to || r.To == "*") && (r.Kind == "any" || r.Kind == kind) {
				return r, true
			}
		}
		return Rule{}, false
	}
	exceptions := make(map[string]Exception, len(m.Exceptions))
	for _, e := range m.Exceptions {
		exceptions[exceptionID(e.From, e.To, e.Kind)] = e
	}
	var violations []Violation
	for _, p := range packages {
		for _, group := range []struct {
			name    string
			imports []string
		}{{"production", p.Imports}, {"test", mergeStrings(p.TestImports, p.XTestImports)}} {
			for _, to := range group.imports {
				fromCat := m.Packages[p.ImportPath]
				toCat, ok := m.Packages[to]
				if !ok {
					if strings.HasPrefix(to, m.Module+"/") {
						return nil, fmt.Errorf("unclassified imported package %s from %s", to, p.ImportPath)
					}
					continue
				}
				_, permitted := allowed(fromCat, toCat, group.name)
				if permitted {
					continue
				}
				id := exceptionID(p.ImportPath, to, group.name)
				if e, exists := exceptions[id]; exists {
					violations = append(violations, Violation{Edge: Edge{p.ImportPath, to, group.name}, Rule: id, Exception: e.ID})
					continue
				}
				violations = append(violations, Violation{Edge: Edge{p.ImportPath, to, group.name}, Rule: fmt.Sprintf("%s -> %s", fromCat, toCat), Remediation: m.DefaultRemediation})
			}
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		a, b := violations[i], violations[j]
		if a.Edge.From != b.Edge.From {
			return a.Edge.From < b.Edge.From
		}
		if a.Edge.To != b.Edge.To {
			return a.Edge.To < b.Edge.To
		}
		return a.Edge.Kind < b.Edge.Kind
	})
	return violations, nil
}

func validateManifest(m Manifest, now time.Time) error {
	categories := make(map[string]bool, len(m.Categories))
	for _, category := range m.Categories {
		if category == "" || categories[category] {
			return fmt.Errorf("malformed category %q", category)
		}
		categories[category] = true
	}
	packagePaths := make(map[string]bool, len(m.Packages))
	for path := range m.Packages {
		packagePaths[path] = true
	}
	for _, path := range sortedKeys(packagePaths) {
		category := m.Packages[path]
		if !categories[category] {
			return fmt.Errorf("package %s uses unknown category %s", path, category)
		}
	}
	ruleIDs := map[string]bool{}
	for _, rule := range m.Rules {
		if rule.From == "" || rule.To == "" || rule.ID == "" || (rule.Kind != "production" && rule.Kind != "test" && rule.Kind != "any") {
			return fmt.Errorf("malformed rule %q", rule.ID)
		}
		if ruleIDs[rule.ID] {
			return fmt.Errorf("duplicate rule %q", rule.ID)
		}
		ruleIDs[rule.ID] = true
		if (rule.From != "*" && !categories[rule.From]) || (rule.To != "*" && !categories[rule.To]) {
			return fmt.Errorf("rule %s names unknown category", rule.ID)
		}
	}
	seen := map[string]bool{}
	canonicalExceptions := map[string]bool{}
	for _, e := range m.Exceptions {
		if e.ID == "" || e.From == "" || e.To == "" || e.Owner == "" || e.Reason == "" || e.Expires == "" || (e.Kind != "production" && e.Kind != "test") {
			return fmt.Errorf("malformed exception %q", e.ID)
		}
		if seen[e.ID] {
			return fmt.Errorf("duplicate exception %q", e.ID)
		}
		seen[e.ID] = true
		canonical := exceptionID(e.From, e.To, e.Kind)
		if canonicalExceptions[canonical] {
			return fmt.Errorf("duplicate exception edge %s", canonical)
		}
		canonicalExceptions[canonical] = true
		expires, err := time.Parse("2006-01-02", e.Expires)
		if err != nil {
			return fmt.Errorf("exception %s has invalid expiry: %w", e.ID, err)
		}
		if !expires.After(now.UTC().Truncate(24 * time.Hour)) {
			return fmt.Errorf("exception %s expired on %s", e.ID, e.Expires)
		}
		if _, ok := m.Packages[e.From]; !ok {
			return fmt.Errorf("exception %s names unknown importer %s", e.ID, e.From)
		}
		if _, ok := m.Packages[e.To]; !ok {
			return fmt.Errorf("exception %s names unknown imported package %s", e.ID, e.To)
		}
	}
	return nil
}

func exceptionID(from, to, kind string) string { return kind + ":" + from + "->" + to }

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func Format(v Violation) string {
	if v.Exception != "" {
		return fmt.Sprintf("architecture: %s -> %s (%s): forbidden direction; live exception %s", v.Edge.From, v.Edge.To, v.Edge.Kind, v.Exception)
	}
	return fmt.Sprintf("architecture: %s -> %s (%s): forbidden rule %s; remediation: %s", v.Edge.From, v.Edge.To, v.Edge.Kind, v.Rule, v.Remediation)
}
