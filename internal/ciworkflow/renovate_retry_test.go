package ciworkflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const (
	renovateTransientLog = `{"level":50,"msg":"lookupUpdates error","err":{"message":"fatal: unable to access 'https://tangled.org/mitchellh.com/go-libghostty/': gnutls_handshake() failed: The TLS connection was non-properly terminated."}}`
	renovateForbiddenLog = `{"level":50,"msg":"lookupUpdates error","err":{"message":"fatal: unable to access 'https://tangled.org/mitchellh.com/go-libghostty/': The requested URL returned error: 403"}}`
	renovateWarningLog   = `{"level":40,"msg":"dreich warning unrelated to the failed lookup"}`
)

type renovateFakeResponse struct {
	log    string
	status int
}

type renovateVerifierResult struct {
	status int
	count  int
	stdout string
	stderr string
}

func TestRenovateRetryPolicy(t *testing.T) {
	tests := map[string]struct {
		responses []renovateFakeResponse
		want      renovateVerifierResult
	}{
		"transient success": {
			responses: []renovateFakeResponse{
				{log: renovateTransientLog, status: 1},
				{log: renovateSuccessLog(t), status: 0},
			},
			want: renovateVerifierResult{
				status: 0,
				count:  2,
				stderr: "retrying Renovate lookup (attempt 2 of 3)",
				stdout: "suppressed the unsupported Ghostty/Highway proposal",
			},
		},
		"warning noise before transient success": {
			responses: []renovateFakeResponse{
				{log: renovateWarningLog + "\n" + renovateTransientLog, status: 1},
				{log: renovateSuccessLog(t), status: 0},
			},
			want: renovateVerifierResult{
				status: 0,
				count:  2,
				stderr: "retrying Renovate lookup (attempt 2 of 3)",
			},
		},
		"deterministic failure": {
			responses: []renovateFakeResponse{
				{log: renovateForbiddenLog, status: 1},
			},
			want: renovateVerifierResult{
				status: 1,
				count:  1,
				stderr: "requested URL returned error: 403",
			},
		},
		"k6 update missing replacement metadata": {
			responses: []renovateFakeResponse{
				{log: renovateSuccessLogWithoutK6ReplacementMetadata(t), status: 0},
			},
			want: renovateVerifierResult{
				status: 1,
				count:  1,
				stderr: "CI tool managers did not retain expected datasource or integrity metadata",
			},
		},
		"goreleaser update skipped": {
			responses: []renovateFakeResponse{
				{log: renovateSuccessLogWithSkippedGoReleaser(t), status: 0},
			},
			want: renovateVerifierResult{
				status: 1,
				count:  1,
				stderr: "CI tool managers did not retain expected datasource or integrity metadata",
			},
		},
		"zig update missing": {
			responses: []renovateFakeResponse{
				{log: renovateSuccessLogWithoutZigUpdate(t), status: 0},
			},
			want: renovateVerifierResult{
				status: 1,
				count:  1,
				stderr: "Zig stopped resolving updates",
			},
		},
		"safehouse dependency missing": {
			responses: []renovateFakeResponse{
				{log: renovateSuccessLogWithoutSafehouseDep(t), status: 0},
			},
			want: renovateVerifierResult{
				status: 1,
				count:  1,
				stderr: "Renovate sandbox workflow dependencies",
			},
		},
		"safehouse review gate missing": {
			responses: []renovateFakeResponse{
				{log: renovateSuccessLogWithoutSafehousePackageRule(t), status: 0},
			},
			want: renovateVerifierResult{
				status: 1,
				count:  1,
				stderr: "Safehouse review gate",
			},
		},
		"deterministic second attempt": {
			responses: []renovateFakeResponse{
				{log: renovateTransientLog, status: 1},
				{log: renovateForbiddenLog, status: 1},
			},
			want: renovateVerifierResult{
				status: 1,
				count:  2,
				stderr: "requested URL returned error: 403",
			},
		},
		"mixed transient and deterministic log": {
			responses: []renovateFakeResponse{
				{log: renovateTransientLog + "\n" + renovateForbiddenLog, status: 1},
			},
			want: renovateVerifierResult{
				status: 1,
				count:  1,
				stderr: "requested URL returned error: 403",
			},
		},
		"three transient failures": {
			responses: []renovateFakeResponse{
				{log: renovateTransientLog, status: 1},
				{log: renovateTransientLog, status: 1},
				{log: renovateTransientLog, status: 1},
			},
			want: renovateVerifierResult{
				status: 1,
				count:  3,
				stderr: "Renovate lookup dry run failed",
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			result := runRenovateVerifier(t, test.responses)
			if result.status != test.want.status {
				t.Fatalf("status = %d, want %d\nstderr:\n%s", result.status, test.want.status, result.stderr)
			}

			if result.count != test.want.count {
				t.Fatalf("attempt count = %d, want %d", result.count, test.want.count)
			}

			if test.want.stdout != "" && !strings.Contains(result.stdout, test.want.stdout) {
				t.Fatalf("stdout missing %q:\n%s", test.want.stdout, result.stdout)
			}

			if test.want.stderr != "" && !strings.Contains(result.stderr, test.want.stderr) {
				t.Fatalf("stderr missing %q:\n%s", test.want.stderr, result.stderr)
			}

			if name == "deterministic second attempt" && strings.Contains(result.stderr, "attempt 3 of 3") {
				t.Fatalf("deterministic second attempt retried again:\n%s", result.stderr)
			}

			if (name == "deterministic failure" || name == "mixed transient and deterministic log") &&
				strings.Contains(result.stderr, "retrying Renovate lookup") {
				t.Fatalf("non-transient failure retried:\n%s", result.stderr)
			}

			if name == "three transient failures" &&
				!strings.Contains(result.stderr, "retrying Renovate lookup (attempt 3 of 3)") {
				t.Fatalf("third transient attempt was not reported:\n%s", result.stderr)
			}
		})
	}
}

func runRenovateVerifier(t *testing.T, responses []renovateFakeResponse) renovateVerifierResult {
	t.Helper()

	repoRoot, err := filepath.Abs(p11RepoRoot())
	if err != nil {
		t.Fatal(err)
	}

	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	responseDir := filepath.Join(tempDir, "responses")
	countFile := filepath.Join(tempDir, "count")

	for _, dir := range []string{binDir, responseDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(countFile, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for index, response := range responses {
		base := filepath.Join(responseDir, strconv.Itoa(index+1))
		if err := os.WriteFile(base+".log", []byte(response.log+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(base+".status", []byte(strconv.Itoa(response.status)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeTestExecutable(t, filepath.Join(binDir, "renovate-config-validator"), "#!/bin/sh\nexit 0\n")
	writeTestExecutable(t, filepath.Join(binDir, "sleep"), "#!/bin/sh\nexit 0\n")
	writeTestExecutable(t, filepath.Join(binDir, "renovate"), `#!/bin/sh
count="$(cat "$FAKE_RENOVATE_COUNT")"
count=$((count + 1))
printf '%s\n' "$count" >"$FAKE_RENOVATE_COUNT"
cat "$FAKE_RENOVATE_RESPONSES/$count.log"
exit "$(cat "$FAKE_RENOVATE_RESPONSES/$count.status")"
`)

	cmd := exec.Command(filepath.Join(repoRoot, "scripts/verify-renovate-libghostty.sh"))
	cmd.Dir = repoRoot

	cmd.Env = append(filteredRenovateVerifierEnv(os.Environ()),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RENOVATE_BIN=renovate",
		"RENOVATE_CONFIG_VALIDATOR_BIN=renovate-config-validator",
		"FAKE_RENOVATE_COUNT="+countFile,
		"FAKE_RENOVATE_RESPONSES="+responseDir,
		"HOME="+tempDir,
		"XDG_CONFIG_HOME="+tempDir,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()

	status := 0

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			status = exitErr.ExitCode()
		} else {
			t.Fatal(err)
		}
	}

	countData, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}

	count, err := strconv.Atoi(strings.TrimSpace(string(countData)))
	if err != nil {
		t.Fatal(err)
	}

	return renovateVerifierResult{
		status: status,
		count:  count,
		stdout: stdout.String(),
		stderr: stderr.String(),
	}
}

func renovateSuccessLog(t *testing.T) string {
	t.Helper()

	ciToolDeps := []any{
		map[string]any{
			"depName":      "github.com/aevea/commitsar",
			"datasource":   "go",
			"currentValue": "v1.0.3",
			"updates": []any{
				map[string]any{"branchName": "renovate/github.com-aevea-commitsar-1.x"},
			},
		},
		map[string]any{
			"depName":       "gitleaks/gitleaks",
			"packageName":   "ghcr.io/gitleaks/gitleaks",
			"datasource":    "docker",
			"currentValue":  "v8.24.3",
			"currentDigest": "sha256:" + strings.Repeat("e", 64),
			"updates": []any{
				map[string]any{
					"branchName": "renovate/gitleaks-gitleaks-8.x",
					"newValue":   "v8.25.1",
					"newDigest":  "sha256:" + strings.Repeat("f", 64),
				},
			},
		},
		map[string]any{
			"depName":      "gohugoio/hugo",
			"datasource":   "github-releases",
			"currentValue": "0.154.5",
			"updates":      []any{},
		},
		map[string]any{
			"depName":       "grafana/k6",
			"datasource":    "docker",
			"currentValue":  "1.8.0-with-browser",
			"currentDigest": "sha256:" + strings.Repeat("a", 64),
			"updates": []any{
				map[string]any{
					"branchName": "renovate/grafana-k6-2.x",
					"newValue":   "2.1.0-with-browser",
					"newDigest":  "sha256:" + strings.Repeat("b", 64),
				},
			},
		},
		map[string]any{
			"depName":      "golang.org/x/vuln/cmd/govulncheck",
			"packageName":  "golang.org/x/vuln",
			"datasource":   "go",
			"currentValue": "v1.3.0",
			"updates": []any{
				map[string]any{"branchName": "renovate/golang.org-x-vuln-cmd-govulncheck-1.x"},
			},
		},
		map[string]any{
			"depName":       "goreleaser/goreleaser",
			"packageName":   "goreleaser/goreleaser",
			"datasource":    "github-releases",
			"currentValue":  "v2.17.1",
			"replaceString": "GORELEASER_VERSION=v2.17.1",
			"updates": []any{
				map[string]any{"branchName": "renovate/goreleaser-goreleaser-2.x"},
			},
		},
		map[string]any{
			"depName":       "ossf/scorecard-action",
			"packageName":   "ghcr.io/ossf/scorecard-action",
			"datasource":    "docker",
			"currentValue":  "v2.4.4",
			"currentDigest": "sha256:" + strings.Repeat("0", 64),
			"updates": []any{
				map[string]any{
					"branchName": "renovate/ossf-scorecard-action-2.x",
					"newValue":   "v2.5.0",
					"newDigest":  "sha256:" + strings.Repeat("1", 64),
				},
			},
		},
		map[string]any{
			"depName":       "trufflesecurity/trufflehog",
			"packageName":   "ghcr.io/trufflesecurity/trufflehog",
			"datasource":    "docker",
			"currentValue":  "3.96.0",
			"currentDigest": "sha256:" + strings.Repeat("c", 64),
			"updates": []any{
				map[string]any{
					"branchName": "renovate/trufflesecurity-trufflehog-3.x",
					"newValue":   "3.97.0",
					"newDigest":  "sha256:" + strings.Repeat("d", 64),
				},
			},
		},
	}

	sandboxDeps := []any{
		map[string]any{
			"depName":      "eugene1g/agent-safehouse",
			"depType":      "ci-safehouse",
			"datasource":   "github-releases",
			"currentValue": "0.11.1",
			"updates":      []any{},
		},
		map[string]any{
			"depName":      "nolabs-ai/nono",
			"datasource":   "github-releases",
			"currentValue": "0.70.0",
			"updates":      []any{},
		},
	}

	nativeDeps := []any{
		map[string]any{
			"depName":       "Ghostty",
			"depType":       "libghostty-native",
			"currentDigest": "d4ac93a0395d321b043ee0116dc8a1a384f0fb83",
			"updates":       []any{},
		},
		map[string]any{
			"depName":      "Highway",
			"depType":      "libghostty-native",
			"currentValue": "1.2.0",
			"updates":      []any{},
		},
	}
	for _, name := range []string{"SPDX tools-java", "go-libghostty", "simdutf", "uucode"} {
		nativeDeps = append(nativeDeps, map[string]any{
			"depName": name,
			"depType": "libghostty-native",
			"updates": []any{
				map[string]any{"branchName": "renovate/libghostty-native"},
			},
		})
	}

	nativeDeps = append(nativeDeps, map[string]any{
		"depName":      "Zig",
		"depType":      "libghostty-native",
		"datasource":   "forgejo-tags",
		"packageName":  "ziglang/zig",
		"registryUrls": []string{"https://codeberg.org/"},
		"updates": []any{
			map[string]any{"branchName": "renovate/libghostty-native"},
		},
	})

	lines := []string{
		renovateJSONLine(t, map[string]any{
			"level": 20,
			"msg":   "packageFiles with updates",
			"config": map[string]any{
				"regex": []any{
					map[string]any{"packageFile": ".github/ci-tool-versions.env", "deps": ciToolDeps},
					map[string]any{"packageFile": ".github/workflows/sandbox.yml", "deps": sandboxDeps},
					map[string]any{"deps": nativeDeps},
				},
			},
		}),
		renovateJSONLine(t, map[string]any{
			"level": 20,
			"msg":   "Repository config",
			"config": map[string]any{
				"packageRules": []any{
					map[string]any{
						"matchDepTypes":               []string{"ci-safehouse"},
						"automerge":                   false,
						"dependencyDashboardApproval": true,
						"prBodyNotes": []string{
							"Review the Safehouse release and update SAFEHOUSE_SHA256 before merge.",
						},
					},
					map[string]any{
						"matchDepTypes":    []string{"libghostty-native"},
						"groupSlug":        "libghostty-native",
						"automerge":        false,
						"postUpgradeTasks": nil,
					},
					map[string]any{
						"matchDepTypes":               []string{"libghostty-native"},
						"matchDepNames":               []string{"go-libghostty", "Ghostty", "Zig", "uucode", "Highway", "simdutf"},
						"dependencyDashboardApproval": true,
					},
					map[string]any{
						"matchDepTypes": []string{"libghostty-native"},
						"matchJsonata": []string{
							"(depName = 'Ghostty' and currentDigest = 'd4ac93a0395d321b043ee0116dc8a1a384f0fb83') or (depName = 'Highway' and currentValue = '1.2.0')",
						},
						"enabled": false,
					},
					map[string]any{
						"matchManagers":     []string{"gomod"},
						"matchPackageNames": []string{"go.mitchellh.com/libghostty"},
						"enabled":           false,
						"automerge":         false,
					},
				},
			},
		}),
	}

	return strings.Join(lines, "\n")
}

func renovateSuccessLogWithoutK6ReplacementMetadata(t *testing.T) string {
	t.Helper()

	log := renovateSuccessLog(t)
	log = strings.ReplaceAll(log, `,"newValue":"2.1.0-with-browser"`, "")
	log = strings.ReplaceAll(log, `,"newDigest":"sha256:`+strings.Repeat("b", 64)+`"`, "")

	return log
}

func renovateSuccessLogWithSkippedGoReleaser(t *testing.T) string {
	t.Helper()

	var lines []string

	for _, line := range strings.Split(renovateSuccessLog(t), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}

		if record["msg"] == "packageFiles with updates" {
			skipGoReleaserUpdate(t, record)
			line = renovateJSONLine(t, record)
		}

		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func renovateSuccessLogWithoutZigUpdate(t *testing.T) string {
	t.Helper()

	var lines []string

	for _, line := range strings.Split(renovateSuccessLog(t), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}

		if record["msg"] == "packageFiles with updates" {
			clearZigUpdate(t, record)
			line = renovateJSONLine(t, record)
		}

		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func renovateSuccessLogWithoutSafehouseDep(t *testing.T) string {
	t.Helper()

	var lines []string

	for _, line := range strings.Split(renovateSuccessLog(t), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}

		if record["msg"] == "packageFiles with updates" {
			removeSafehouseDep(t, record)
			line = renovateJSONLine(t, record)
		}

		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func renovateSuccessLogWithoutSafehousePackageRule(t *testing.T) string {
	t.Helper()

	var lines []string

	for _, line := range strings.Split(renovateSuccessLog(t), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}

		if record["msg"] == "Repository config" {
			removeSafehousePackageRule(t, record)
			line = renovateJSONLine(t, record)
		}

		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func clearZigUpdate(t *testing.T, record map[string]any) {
	t.Helper()

	config, ok := record["config"].(map[string]any)
	if !ok {
		t.Fatal("Renovate log missing config object")
	}

	regexManagers, ok := config["regex"].([]any)
	if !ok {
		t.Fatal("Renovate log missing regex managers")
	}

	for _, managerAny := range regexManagers {
		manager, ok := managerAny.(map[string]any)
		if !ok {
			t.Fatal("Renovate regex manager was not an object")
		}

		deps, ok := manager["deps"].([]any)
		if !ok {
			continue
		}

		for _, depAny := range deps {
			dep, ok := depAny.(map[string]any)
			if !ok {
				t.Fatal("Renovate dependency was not an object")
			}

			if dep["depName"] == "Zig" {
				dep["updates"] = []any{}
				return
			}
		}
	}

	t.Fatal("Renovate log missing Zig dependency")
}

func skipGoReleaserUpdate(t *testing.T, record map[string]any) {
	t.Helper()

	config, ok := record["config"].(map[string]any)
	if !ok {
		t.Fatal("Renovate log missing config object")
	}

	regexManagers, ok := config["regex"].([]any)
	if !ok {
		t.Fatal("Renovate log missing regex managers")
	}

	for _, managerAny := range regexManagers {
		manager, ok := managerAny.(map[string]any)
		if !ok {
			t.Fatal("Renovate regex manager was not an object")
		}

		deps, ok := manager["deps"].([]any)
		if !ok {
			continue
		}

		for _, depAny := range deps {
			dep, ok := depAny.(map[string]any)
			if !ok {
				t.Fatal("Renovate dependency was not an object")
			}

			if dep["depName"] == "goreleaser/goreleaser" {
				dep["skipReason"] = "invalid-version"
				return
			}
		}
	}

	t.Fatal("Renovate log missing GoReleaser dependency")
}

func removeSafehouseDep(t *testing.T, record map[string]any) {
	t.Helper()

	config, ok := record["config"].(map[string]any)
	if !ok {
		t.Fatal("Renovate log missing config object")
	}

	regexManagers, ok := config["regex"].([]any)
	if !ok {
		t.Fatal("Renovate log missing regex managers")
	}

	for _, managerAny := range regexManagers {
		manager, ok := managerAny.(map[string]any)
		if !ok {
			t.Fatal("Renovate regex manager was not an object")
		}

		if manager["packageFile"] != ".github/workflows/sandbox.yml" {
			continue
		}

		deps, ok := manager["deps"].([]any)
		if !ok {
			t.Fatal("Renovate sandbox manager missing deps")
		}

		filtered := make([]any, 0, len(deps))
		removed := false

		for _, depAny := range deps {
			dep, ok := depAny.(map[string]any)
			if !ok {
				t.Fatal("Renovate dependency was not an object")
			}

			if dep["depName"] == "eugene1g/agent-safehouse" {
				removed = true
				continue
			}

			filtered = append(filtered, depAny)
		}

		if !removed {
			t.Fatal("Renovate log missing Safehouse dependency")
		}

		manager["deps"] = filtered

		return
	}

	t.Fatal("Renovate log missing sandbox workflow manager")
}

func removeSafehousePackageRule(t *testing.T, record map[string]any) {
	t.Helper()

	config, ok := record["config"].(map[string]any)
	if !ok {
		t.Fatal("Renovate log missing config object")
	}

	rules, ok := config["packageRules"].([]any)
	if !ok {
		t.Fatal("Renovate config missing packageRules")
	}

	filtered := make([]any, 0, len(rules))
	removed := false

	for _, ruleAny := range rules {
		rule, ok := ruleAny.(map[string]any)
		if !ok {
			t.Fatal("Renovate package rule was not an object")
		}

		if reflect.DeepEqual(rule["matchDepTypes"], []any{"ci-safehouse"}) {
			removed = true
			continue
		}

		filtered = append(filtered, ruleAny)
	}

	if !removed {
		t.Fatal("Renovate config missing Safehouse package rule")
	}

	config["packageRules"] = filtered
}

func renovateJSONLine(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func filteredRenovateVerifierEnv(environ []string) []string {
	omit := map[string]bool{
		"GIT_CONFIG_COUNT":    true,
		"GIT_CONFIG_GLOBAL":   true,
		"GIT_CONFIG_NOSYSTEM": true,
		"HOME":                true,
		"XDG_CONFIG_HOME":     true,
	}

	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			filtered = append(filtered, entry)
			continue
		}

		if omit[key] || strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}

		filtered = append(filtered, entry)
	}

	return filtered
}

func writeTestExecutable(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o755); err != nil { //nolint:gosec // G302: fake command must be executable for script policy coverage.
		t.Fatal(err)
	}
}
