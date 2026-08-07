package config

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const NotificationInstructionTemplateMaxBytes = 4096

const (
	NotificationKindGithubCIFailure        = "github_ci_failure"
	NotificationKindGithubCIComplete       = "github_ci_complete"
	NotificationKindGithubCIRecovery       = "github_ci_recovery"
	NotificationKindGithubPRMergeConflict  = "github_pr_merge_conflict"
	NotificationKindGithubPRLifecycle      = "github_pr_lifecycle"
	NotificationKindGithubPRReviewDecision = "github_pr_review_decision"
	NotificationKindGithubPRReview         = "github_pr_review"
	NotificationKindGithubPRComment        = "github_pr_comment"
)

var notificationInstructionTemplatePattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

var notificationInstructionKindSet = map[string]bool{
	NotificationKindGithubCIFailure:        true,
	NotificationKindGithubCIComplete:       true,
	NotificationKindGithubCIRecovery:       true,
	NotificationKindGithubPRMergeConflict:  true,
	NotificationKindGithubPRLifecycle:      true,
	NotificationKindGithubPRReviewDecision: true,
	NotificationKindGithubPRReview:         true,
	NotificationKindGithubPRComment:        true,
}

func NotificationInstructionKinds() []string {
	return []string{
		NotificationKindGithubCIFailure,
		NotificationKindGithubCIComplete,
		NotificationKindGithubCIRecovery,
		NotificationKindGithubPRMergeConflict,
		NotificationKindGithubPRLifecycle,
		NotificationKindGithubPRReviewDecision,
		NotificationKindGithubPRReview,
		NotificationKindGithubPRComment,
	}
}

// NotificationInstructionRule is one trusted user-configured guidance rule for
// daemon-authored system notifications. All non-empty condition lists are ANDed;
// values inside one list are ORed. Matching rules append in config order.
type NotificationInstructionRule struct {
	Name         string   `toml:"name"`
	Kinds        []string `toml:"kinds"`
	Owners       []string `toml:"owners"`
	Repos        []string `toml:"repos"`
	Authors      []string `toml:"authors"`
	SessionNames []string `toml:"session_names"`
	SessionRepos []string `toml:"session_repos"`
	Template     string   `toml:"template"`
}

// NotificationInstructionContext carries trusted event metadata used for rule
// matching and template variables. It intentionally has no free-form event body
// field, so PR/comment text cannot be rendered into an instruction position.
type NotificationInstructionContext struct {
	Kind        string
	Repo        string
	Authors     []string
	PRNumber    int
	URL         string
	SessionName string
	SessionRepo string
}

type RenderedNotificationInstruction struct {
	Name string
	Text string
}

func rejectUnknownNotificationInstructionKeys(data []byte) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil // the typed decode reports structural TOML errors
	}

	value, ok := raw["notification_instruction"]
	if !ok {
		return nil
	}

	allowed := map[string]bool{
		"name":          true,
		"kinds":         true,
		"owners":        true,
		"repos":         true,
		"authors":       true,
		"session_names": true,
		"session_repos": true,
		"template":      true,
	}

	var errs []error

	for i, table := range notificationInstructionRawTables(value) {
		for key := range table {
			if !allowed[strings.ToLower(key)] {
				errs = append(errs, fmt.Errorf("notification_instruction[%d].%s: unsupported condition or field", i, key))
			}
		}
	}

	return errors.Join(errs...)
}

func notificationInstructionRawTables(value any) []map[string]any {
	switch rules := value.(type) {
	case []map[string]any:
		return rules
	case []any:
		out := make([]map[string]any, 0, len(rules))
		for _, rule := range rules {
			if table, ok := rule.(map[string]any); ok {
				out = append(out, table)
			}
		}

		return out
	default:
		return nil
	}
}

func validateNotificationInstructionRules(rules []NotificationInstructionRule) []error {
	var errs []error

	names := make(map[string]int, len(rules))

	for i, rule := range rules {
		field := func(name string) string {
			return fmt.Sprintf("notification_instruction[%d].%s", i, name)
		}

		name := strings.TrimSpace(rule.Name)
		if name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", field("name")))
		} else {
			key := strings.ToLower(name)
			if prev, ok := names[key]; ok {
				errs = append(errs, fmt.Errorf("%s %q: duplicates notification_instruction[%d].name", field("name"), name, prev))
			} else {
				names[key] = i
			}
		}

		if !rule.hasAnyCondition() {
			errs = append(errs, fmt.Errorf("notification_instruction[%d]: at least one of kinds, owners, repos, authors, session_names, or session_repos is required", i))
		}

		errs = append(errs, validateNotificationKindValues(field("kinds"), rule.Kinds)...)
		errs = append(errs, validateNotificationOwnerValues(field("owners"), rule.Owners)...)
		errs = append(errs, validateNotificationRepoValues(field("repos"), rule.Repos)...)
		errs = append(errs, validateNotificationMatchValues(field("authors"), rule.Authors, false)...)
		errs = append(errs, validateNotificationMatchValues(field("session_names"), rule.SessionNames, true)...)
		errs = append(errs, validateNotificationRepoValues(field("session_repos"), rule.SessionRepos)...)

		if err := validateNotificationInstructionTemplate(field("template"), rule.Template); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

func (r NotificationInstructionRule) hasAnyCondition() bool {
	return len(r.Kinds) > 0 || len(r.Owners) > 0 || len(r.Repos) > 0 || len(r.Authors) > 0 ||
		len(r.SessionNames) > 0 || len(r.SessionRepos) > 0
}

func validateNotificationMatchValues(field string, values []string, allowSpaces bool) []error {
	var errs []error

	for i, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			errs = append(errs, fmt.Errorf("%s[%d]: value must not be empty", field, i))
			continue
		}

		if strings.ContainsAny(trimmed, "\r\n") {
			errs = append(errs, fmt.Errorf("%s[%d] %q: value must not contain newlines", field, i, value))
		}

		if !allowSpaces && strings.ContainsAny(trimmed, " \t") {
			errs = append(errs, fmt.Errorf("%s[%d] %q: value must not contain whitespace", field, i, value))
		}
	}

	return errs
}

func validateNotificationKindValues(field string, values []string) []error {
	errs := validateNotificationMatchValues(field, values, false)

	for i, value := range values {
		kind := strings.ToLower(strings.TrimSpace(value))
		if kind == "" {
			continue
		}

		if !notificationInstructionKindSet[kind] {
			errs = append(errs, fmt.Errorf("%s[%d] %q: unsupported notification kind; supported values are %s", field, i, value, strings.Join(NotificationInstructionKinds(), ", ")))
		}
	}

	return errs
}

func validateNotificationRepoValues(field string, values []string) []error {
	errs := validateNotificationMatchValues(field, values, false)

	for i, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") ||
			strings.Contains(trimmed, "//") || strings.Count(trimmed, "/") > 1 {
			errs = append(errs, fmt.Errorf("%s[%d] %q: value must be a repo basename or owner/repo with at most one slash and no leading, trailing, or repeated slash", field, i, value))
		}
	}

	return errs
}

func validateNotificationOwnerValues(field string, values []string) []error {
	errs := validateNotificationMatchValues(field, values, false)

	for i, value := range values {
		if strings.Contains(strings.TrimSpace(value), "/") {
			errs = append(errs, fmt.Errorf("%s[%d] %q: value must be an owner or user name, not owner/repo", field, i, value))
		}
	}

	return errs
}

func validateNotificationInstructionTemplate(field, tmpl string) error {
	if strings.TrimSpace(tmpl) == "" {
		return fmt.Errorf("%s: template is required", field)
	}

	if len([]byte(tmpl)) > NotificationInstructionTemplateMaxBytes {
		return fmt.Errorf("%s: template must be %d bytes or fewer", field, NotificationInstructionTemplateMaxBytes)
	}

	var unknown []string

	seen := map[string]bool{}

	withoutVars := notificationInstructionTemplatePattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		parts := notificationInstructionTemplatePattern.FindStringSubmatch(match)
		if len(parts) == 2 && !notificationInstructionTemplateVars()[parts[1]] && !seen[parts[1]] {
			unknown = append(unknown, parts[1])
			seen[parts[1]] = true
		}

		return ""
	})

	if strings.Contains(withoutVars, "{{") || strings.Contains(withoutVars, "}}") {
		return fmt.Errorf("%s: malformed template variable (use {{kind}}, {{repo}}, {{author}}, {{pr_number}}, {{url}}, {{session_name}}, or {{session_repo}})", field)
	}

	if len(unknown) > 0 {
		return fmt.Errorf("%s: unknown template variable %q", field, unknown[0])
	}

	return nil
}

func notificationInstructionTemplateVars() map[string]bool {
	return map[string]bool{
		"kind":         true,
		"repo":         true,
		"author":       true,
		"pr_number":    true,
		"url":          true,
		"session_name": true,
		"session_repo": true,
	}
}

func RenderNotificationInstructions(rules []NotificationInstructionRule, ctx NotificationInstructionContext) ([]RenderedNotificationInstruction, error) {
	if len(rules) == 0 {
		return nil, nil
	}

	var rendered []RenderedNotificationInstruction

	for i, rule := range rules {
		if !rule.MatchesNotification(ctx) {
			continue
		}

		text, err := renderNotificationInstructionTemplate(rule.Template, ctx)
		if err != nil {
			return nil, fmt.Errorf("notification_instruction[%d].template: %w", i, err)
		}

		rendered = append(rendered, RenderedNotificationInstruction{
			Name: strings.TrimSpace(rule.Name),
			Text: strings.TrimSpace(text),
		})
	}

	return rendered, nil
}

func (r NotificationInstructionRule) MatchesNotification(ctx NotificationInstructionContext) bool {
	return matchStringCondition(r.Kinds, ctx.Kind) &&
		matchOwnerCondition(r.Owners, ctx.Repo, ctx.SessionRepo) &&
		matchRepoCondition(r.Repos, ctx.Repo) &&
		matchAuthorCondition(r.Authors, ctx.Authors) &&
		matchStringCondition(r.SessionNames, ctx.SessionName) &&
		matchRepoCondition(r.SessionRepos, ctx.SessionRepo)
}

func matchStringCondition(wants []string, got string) bool {
	if len(wants) == 0 {
		return true
	}

	got = strings.ToLower(strings.TrimSpace(got))
	if got == "" {
		return false
	}

	for _, want := range wants {
		if strings.ToLower(strings.TrimSpace(want)) == got {
			return true
		}
	}

	return false
}

func matchAuthorCondition(wants []string, got []string) bool {
	if len(wants) == 0 {
		return true
	}

	gotSet := make(map[string]bool, len(got))
	for _, author := range got {
		author = strings.ToLower(strings.TrimSpace(author))
		if author != "" {
			gotSet[author] = true
		}
	}

	for _, want := range wants {
		if gotSet[strings.ToLower(strings.TrimSpace(want))] {
			return true
		}
	}

	return false
}

func matchOwnerCondition(wants []string, repo, sessionRepo string) bool {
	if len(wants) == 0 {
		return true
	}

	got := repoOwner(repo)
	if got == "" {
		got = repoOwner(sessionRepo)
	}

	if got == "" {
		return false
	}

	for _, want := range wants {
		if strings.ToLower(strings.TrimSpace(want)) == got {
			return true
		}
	}

	return false
}

func matchRepoCondition(wants []string, got string) bool {
	if len(wants) == 0 {
		return true
	}

	got = normalizeNotificationRepo(got)
	if got == "" {
		return false
	}

	for _, want := range wants {
		want = normalizeNotificationRepo(want)
		if want == "" {
			continue
		}

		if want == got {
			return true
		}

		if !strings.Contains(want, "/") && repoBase(got) == want {
			return true
		}
	}

	return false
}

func normalizeNotificationRepo(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, ".git")

	return strings.Trim(value, "/")
}

func repoOwner(value string) string {
	value = normalizeNotificationRepo(value)

	owner, _, ok := strings.Cut(value, "/")
	if !ok || owner == "" {
		return ""
	}

	return owner
}

func repoBase(value string) string {
	value = strings.Trim(value, "/")
	if value == "" {
		return ""
	}

	return strings.ToLower(path.Base(value))
}

func renderNotificationInstructionTemplate(tmpl string, ctx NotificationInstructionContext) (string, error) {
	values := notificationInstructionValues(ctx)

	var renderErr error

	rendered := notificationInstructionTemplatePattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		parts := notificationInstructionTemplatePattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			renderErr = fmt.Errorf("malformed template variable %q", match)
			return match
		}

		value, ok := values[parts[1]]
		if !ok {
			renderErr = fmt.Errorf("unknown template variable %q", parts[1])
			return match
		}

		return value
	})

	if renderErr != nil {
		return "", renderErr
	}

	if strings.Contains(rendered, "{{") || strings.Contains(rendered, "}}") {
		return "", errors.New("malformed template variable")
	}

	return rendered, nil
}

func notificationInstructionValues(ctx NotificationInstructionContext) map[string]string {
	prNumber := ""
	if ctx.PRNumber > 0 {
		prNumber = strconv.Itoa(ctx.PRNumber)
	}

	return map[string]string{
		"kind":         notificationDataValue(ctx.Kind),
		"repo":         notificationDataValue(ctx.Repo),
		"author":       notificationDataValue(strings.Join(uniqueNonEmpty(ctx.Authors), ", ")),
		"pr_number":    prNumber,
		"url":          notificationDataValue(ctx.URL),
		"session_name": notificationDataValue(ctx.SessionName),
		"session_repo": notificationDataValue(ctx.SessionRepo),
	}
}

func notificationDataValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer(
		"\r", " ",
		"\n", " ",
		"\t", " ",
		"{{", "{ {",
		"}}", "} }",
	).Replace(value)

	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 512 {
		return value[:512] + "..."
	}

	return value
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))

	for _, value := range values {
		value = notificationDataValue(value)

		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}

		seen[key] = true

		out = append(out, value)
	}

	return out
}
