package cipolicy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/d0ugal/graith/internal/cibaseline"
)

type ShadowSummaryInput struct {
	Inventory           cibaseline.Inventory
	Plan                *RunPlan
	PlanError           string
	ChangedFiles        []string
	EventName           string
	Ref                 string
	HeadSHA             string
	RunURL              string
	MacOSDetectorResult string
	MacOSDetectorOutput string
}

var shadowCapabilityLabels = map[string]string{
	"commit-policy":              "commit policy",
	"coverage":                   "coverage",
	"dev-release":                "development release",
	"docs-preview":               "documentation preview",
	"docs-publication":           "documentation publication",
	"generated-metadata":         "generated metadata",
	"go-core":                    "Go core",
	"gui":                        "GUI/iOS",
	"native":                     "libghostty/native core runtime",
	"native-publication":         "native publication",
	"release-automation":         "release automation",
	"sandbox":                    "sandbox isolation",
	"security-codeql":            "CodeQL security",
	"security-dependency-review": "dependency review security",
	"security-scorecard":         "OpenSSF Scorecard security",
	"security-secret-scan":       "secret scanning",
	"stable-release":             "stable release",
	"workflow-policy":            "workflow policy",
}

var shadowReasonLabels = map[string]string{
	"ci-policy-change":  "CI policy or workflow helper changed, so the detector escalates to the safe superset.",
	"detector-error":    "Changed-file parsing or detector execution reported an error.",
	"empty-file-list":   "The detector received an exact but empty changed-file list.",
	"file-list-unknown": "The changed-file list is unavailable, so the detector uses the safe superset.",
	"generated-input":   "Generated-file inputs changed and require broad validation.",
	"lockfile":          "A lockfile changed and requires broad dependency validation.",
	"release-metadata":  "Release metadata changed and requires release-shaped validation.",
	"unknown-path":      "At least one path is not covered by checked-in detector rules.",
}

func RenderShadowSummary(input ShadowSummaryInput) (string, error) {
	if err := input.Inventory.Validate(); err != nil {
		return "", fmt.Errorf("validate inventory for CI shadow summary: %w", err)
	}

	var b strings.Builder

	workflowCount := len(input.Inventory.Workflows)
	jobCount := 0

	for _, workflow := range input.Inventory.Workflows {
		jobCount += len(workflow.Jobs)
	}

	writeLines(&b,
		"# CI shadow summary",
		"",
		"> Diagnostic only. Current required checks still decide mergeability. This job uses `permissions: contents: read`, receives no publication credentials, and is not a source-isolated PR gate.",
		"",
		"## Source",
		"",
		bulletList([]string{
			fmt.Sprintf("event: `%s`", defaultString(input.EventName, "unknown")),
			fmt.Sprintf("ref: `%s`", defaultString(input.Ref, "unknown")),
			fmt.Sprintf("reported head SHA: `%s`", defaultString(input.HeadSHA, "unknown")),
			fmt.Sprintf("inventory digest: `%s`", defaultString(input.Inventory.Digest, "unknown")),
		}),
		"",
		"## PR change classes",
		"",
		bulletList(summarizeShadowChangeClasses(input.Plan, input.ChangedFiles)),
		"",
		"## Local detector decisions",
		"",
		markdownTable([]string{"Detector", "Result", "Decision"}, summarizeShadowDetector(input.Plan, input.PlanError, input)),
		"",
		"## Skip and escalation reasons",
		"",
		bulletList(summarizeShadowReasons(input.Plan, input.PlanError)),
		"",
		"## Required contexts",
		"",
		bulletList(backticked(input.Inventory.RequiredContexts)),
		"",
		"## Expected workflows and jobs",
		"",
		fmt.Sprintf("%d workflows and %d jobs are recorded in `internal/cibaseline/inventory.json`.", workflowCount, jobCount),
		"",
		"<details>",
		"<summary>Workflow/job inventory</summary>",
		"",
		markdownTable([]string{"Workflow", "Path", "Jobs"}, workflowRows(input.Inventory)),
		"",
		"</details>",
		"",
		"## Workflow helper surfaces",
		"",
		"<details>",
		"<summary>Repository-owned helper scripts and language surfaces</summary>",
		"",
		markdownTable([]string{"Language", "Kind", "Owner", "Path"}, helperRows(input.Inventory)),
		"",
		"</details>",
		"",
		"## Core runtime and timing notes",
		"",
		bulletList([]string{
			"`libghostty/native` is core runtime validation. The required `Native backend gate` remains authoritative for native source-build, artifact, manifest, archive, and consumer coverage.",
			actionsUILine(input.RunURL),
			"This summary does not aggregate repository-wide observed job results, retained history, cross-workflow durations, or live check-run completion state.",
		}),
		"",
	)

	return b.String(), nil
}

func summarizeShadowChangeClasses(plan *RunPlan, changedFiles []string) []string {
	if plan == nil {
		return []string{
			"Static inventory only; the local policy detector did not produce a plan.",
			fmt.Sprintf("Changed files visible to the job: %d", len(changedFiles)),
		}
	}

	var classes []string
	if len(plan.DetectedCapabilities) == 0 {
		classes = append(classes, "No narrow file-class match beyond the current required PR floor.")
	} else {
		for _, capability := range plan.DetectedCapabilities {
			classes = append(classes, fmt.Sprintf("%s (%s)", shadowCapabilityLabel(capability), capability))
		}
	}

	if plan.Superset {
		reasons := "reason unavailable"
		if len(plan.SupersetReasons) > 0 {
			reasons = strings.Join(plan.SupersetReasons, ", ")
		}

		classes = append(classes, "Safe-superset selected: "+reasons+".")
	}

	classes = append(classes, fmt.Sprintf("Changed files visible to the job: %d", len(changedFiles)))

	return classes
}

func summarizeShadowDetector(plan *RunPlan, planError string, input ShadowSummaryInput) [][]string {
	macosResult := defaultString(input.MacOSDetectorResult, "unknown")
	macosOutput := input.MacOSDetectorOutput
	macosDecision := "Detector result unavailable to this summary."

	switch {
	case macosResult == "success" && macosOutput == "true":
		macosDecision = "macOS-relevant changes detected; macOS build/test/integration jobs run."
	case macosResult == "success" && macosOutput == "false":
		macosDecision = "No macOS-relevant paths detected; macOS jobs are skipped at job level."
	case macosResult == "success":
		macosDecision = "macOS detector succeeded but did not expose a recognized output value."
	case macosResult != "unknown":
		macosDecision = "Detector did not succeed; dependent macOS jobs fail safe toward running."
	}

	rows := [][]string{{"macOS detector", macosResult, macosDecision}}
	if plan == nil {
		rows = append(rows, []string{"cipolicy detector", "unavailable", firstNonEmptyLine(planError, "plan output was not produced")})
		return rows
	}

	result := "narrow"
	if plan.Superset {
		result = "safe superset"
	}

	rows = append(rows, []string{
		"cipolicy detector",
		result,
		fmt.Sprintf("version %s; exact file list: %s; digest %s",
			defaultString(plan.DetectorVersion, "unknown"),
			yesNo(plan.ExactFileList),
			defaultString(plan.DetectorDigest, "unknown"),
		),
	})

	return rows
}

func summarizeShadowReasons(plan *RunPlan, planError string) []string {
	if plan == nil {
		return []string{firstNonEmptyLine(planError, "No plan was produced.")}
	}

	if len(plan.SupersetReasons) == 0 {
		return []string{"No safe-superset escalation reason was reported by the local policy detector."}
	}

	reasons := make([]string, 0, len(plan.SupersetReasons))
	for _, reason := range plan.SupersetReasons {
		reasons = append(reasons, reason+": "+defaultString(shadowReasonLabels[reason], "No description available."))
	}

	return reasons
}

func workflowRows(inventory cibaseline.Inventory) [][]string {
	rows := make([][]string, 0, len(inventory.Workflows))
	for _, workflow := range inventory.Workflows {
		jobs := make([]string, 0, len(workflow.Jobs))
		for _, job := range workflow.Jobs {
			jobs = append(jobs, job.Name)
		}

		rows = append(rows, []string{workflow.Name, workflow.Path, strings.Join(jobs, ", ")})
	}

	return rows
}

func helperRows(inventory cibaseline.Inventory) [][]string {
	var rows [][]string

	for _, surface := range inventory.Surfaces {
		if !isHelperSurface(surface) {
			continue
		}

		rows = append(rows, []string{
			helperLanguage(surface.Path),
			surface.Kind,
			surface.Owner,
			surface.Path,
		})
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i][3] < rows[j][3] })

	return rows
}

func isHelperSurface(surface cibaseline.Surface) bool {
	kind := surface.Kind

	return strings.Contains(kind, "helper") ||
		strings.Contains(kind, "script") ||
		strings.Contains(kind, "contract") ||
		kind == "ci-entrypoint" ||
		strings.HasPrefix(surface.Path, "scripts/") ||
		strings.HasPrefix(surface.Path, ".github/workflows/scripts/") ||
		strings.HasPrefix(surface.Path, "cmd/cipolicy/") ||
		strings.HasPrefix(surface.Path, "internal/cipolicy/")
}

func helperLanguage(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "Go"
	case strings.HasSuffix(path, ".js"):
		return "JavaScript"
	case strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".json5"):
		return "JSON"
	case strings.HasSuffix(path, ".sh"):
		return "Shell"
	case strings.HasSuffix(path, ".py"):
		return "Python"
	case strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml"):
		return "YAML"
	case strings.HasSuffix(path, ".toml"):
		return "TOML"
	case strings.HasSuffix(path, ".lock"):
		return "Lockfile"
	case strings.HasSuffix(path, "Makefile") || path == "Makefile":
		return "Make"
	default:
		return "Other"
	}
}

func markdownTable(headers []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString("| ")
	b.WriteString(strings.Join(escapedMarkdownCells(headers), " | "))
	b.WriteString(" |\n| ")
	b.WriteString(strings.Join(repeated("---", len(headers)), " | "))
	b.WriteString(" |")

	for _, row := range rows {
		b.WriteString("\n| ")
		b.WriteString(strings.Join(escapedMarkdownCells(row), " | "))
		b.WriteString(" |")
	}

	return b.String()
}

func escapedMarkdownCells(values []string) []string {
	cells := make([]string, len(values))
	for index, value := range values {
		value = strings.ReplaceAll(value, "|", `\|`)
		value = strings.ReplaceAll(value, "\n", "<br>")
		cells[index] = value
	}

	return cells
}

func bulletList(values []string) string {
	if len(values) == 0 {
		return "- none"
	}

	var b strings.Builder

	for index, value := range values {
		if index > 0 {
			b.WriteByte('\n')
		}

		b.WriteString("- ")
		b.WriteString(value)
	}

	return b.String()
}

func backticked(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = "`" + value + "`"
	}

	return result
}

func actionsUILine(runURL string) string {
	if runURL == "" {
		return "Use the normal Actions run UI for job durations and logs."
	}

	return "Use the normal Actions run UI for job durations and logs: " + runURL
}

func shadowCapabilityLabel(capability string) string {
	if label := shadowCapabilityLabels[capability]; label != "" {
		return label
	}

	return capability
}

func firstNonEmptyLine(value, fallback string) string {
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}

	return fallback
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}

func repeated(value string, count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = value
	}

	return values
}

func writeLines(b *strings.Builder, lines ...string) {
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
}
