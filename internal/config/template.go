package config

import (
	"fmt"
	"regexp"
)

var varPattern = regexp.MustCompile(`\{(\w+)\}`)

type TemplateVars struct {
	Username                 string
	AgentSessionID           string
	SessionName              string
	SessionID                string
	WorktreePath             string
	ForkSourceAgentSessionID string
	Model                    string
	// Dir is the directory bound to {dir} when expanding an agent's
	// add_dir_args, once per granted worktree (see Agent.AddDirArgsFor). It is
	// empty in every other expansion context.
	Dir string
	// Profile, ReasoningEffort, ServiceTier, ApprovalPolicy, and WebSearch are
	// the Codex per-session options (issue #1186) surfaced as template variables
	// so an agent's conditional option_args groups (Agent.OptionArgsFor) can turn
	// them into CLI flags from config rather than hard-coded Go (issue #1236).
	// WebSearch is a boolean; it expands to "true" when set and "" otherwise, so
	// an option_args group can gate on it with `when = "web_search"`.
	Profile         string
	ReasoningEffort string
	ServiceTier     string
	ApprovalPolicy  string
	WebSearch       bool
}

func (v TemplateVars) toMap() map[string]string {
	return map[string]string{
		"username":                     v.Username,
		"agent_session_id":             v.AgentSessionID,
		"session_name":                 v.SessionName,
		"session_id":                   v.SessionID,
		"worktree_path":                v.WorktreePath,
		"fork_source_agent_session_id": v.ForkSourceAgentSessionID,
		"model":                        v.Model,
		"dir":                          v.Dir,
		"profile":                      v.Profile,
		"reasoning_effort":             v.ReasoningEffort,
		"service_tier":                 v.ServiceTier,
		"approval_policy":              v.ApprovalPolicy,
		"web_search":                   boolVar(v.WebSearch),
	}
}

// boolVar renders a boolean template variable so a conditional option-args group
// can gate on it: "true" when set, "" (empty, i.e. "unset") otherwise.
func boolVar(b bool) string {
	if b {
		return "true"
	}

	return ""
}

// IsTemplateVar reports whether name is a known template variable (one of the
// keys TemplateVars expands). Used to validate an agent's option_args `when`
// gate so a typo (`when = "reasoning"`) is caught at config-load time rather
// than silently never firing.
func IsTemplateVar(name string) bool {
	_, ok := (TemplateVars{}).toMap()[name]

	return ok
}

func Expand(s string, vars TemplateVars) (string, error) {
	lookup := vars.toMap()

	var expandErr error

	result := varPattern.ReplaceAllStringFunc(s, func(match string) string {
		key := match[1 : len(match)-1]

		val, ok := lookup[key]
		if !ok {
			expandErr = fmt.Errorf("unknown template variable %q in %q", key, s)
			return match
		}

		return val
	})

	return result, expandErr
}

func ExpandSlice(ss []string, vars TemplateVars) ([]string, error) {
	out := make([]string, len(ss))
	for i, s := range ss {
		expanded, err := Expand(s, vars)
		if err != nil {
			return nil, err
		}

		out[i] = expanded
	}

	return out, nil
}

// ExpandWith expands {key} tokens in s from subs, erroring on any token not in
// subs. Unlike Expand it uses a caller-supplied variable set rather than the
// fixed session TemplateVars: it drives the agent adapter argv templates (hook,
// MCP, and prompt-injection spellings, issue #1236) whose dynamic values —
// generated file paths and encoded config blobs — are built in Go and bound per
// call. Only the original template is scanned for tokens, so a substituted value
// that itself contains braces (an encoded prompt, JSON args) is inserted
// verbatim and never re-expanded.
func ExpandWith(s string, subs map[string]string) (string, error) {
	var expandErr error

	result := varPattern.ReplaceAllStringFunc(s, func(match string) string {
		key := match[1 : len(match)-1]

		val, ok := subs[key]
		if !ok {
			expandErr = fmt.Errorf("unknown template variable %q in %q", key, s)
			return match
		}

		return val
	})

	return result, expandErr
}

// templateTokens returns the distinct {key} variable names referenced across
// ss. It backs adapter-template validation (issue #1236) so a template using an
// unsupported placeholder is rejected at config load rather than failing a
// launch.
func templateTokens(ss []string) []string {
	seen := map[string]bool{}

	var out []string

	for _, s := range ss {
		for _, m := range varPattern.FindAllStringSubmatch(s, -1) {
			if key := m[1]; !seen[key] {
				seen[key] = true

				out = append(out, key)
			}
		}
	}

	return out
}

// ExpandSliceWith applies ExpandWith to each element of ss.
func ExpandSliceWith(ss []string, subs map[string]string) ([]string, error) {
	if ss == nil {
		return nil, nil
	}

	out := make([]string, len(ss))
	for i, s := range ss {
		expanded, err := ExpandWith(s, subs)
		if err != nil {
			return nil, err
		}

		out[i] = expanded
	}

	return out, nil
}
