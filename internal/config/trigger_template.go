package config

import "fmt"

// TriggerVars is the variable set available to trigger delivery/message
// templates. It is deliberately separate from TemplateVars (which is a fixed
// struct for agent-arg expansion) — trigger templates have their own tokens and
// must not silently accept agent-arg names. Like Expand, ExpandTrigger errors on
// an unknown {token}.
type TriggerVars struct {
	Name            string // trigger name
	Date            string // e.g. 2026-07-11
	Datetime        string // RFC3339
	FireTime        string // scheduled/observed fire instant (RFC3339)
	SessionName     string // watch source: the bound session
	WorktreePath    string // watch source: the bound session's worktree
	ChangedFiles    string // watch source: comma-separated changed paths (or "")
	ChangeCount     string // watch source: number of changed paths
	ScenarioID      string // completion source: owning scenario ID
	ScenarioName    string // completion source: owning scenario name
	CompletionEpoch string // completion source: monotonically increasing epoch
	// Tracker action: the issue a spawned session is seeded from. These are known
	// template tokens (they live in this shared struct), so they expand to the
	// empty string — not an error — outside a tracker prompt; a genuinely unknown
	// token still errors.
	IssueNumber string // e.g. "643"
	IssueTitle  string
	IssueBody   string
	IssueURL    string
	IssueLabels string // comma-separated label names (or "")
	// GCX source: stable, structured event metadata. Raw alert title, labels,
	// annotations, and subject are deliberately excluded because external alert
	// text is untrusted input to an autonomous agent.
	GCXEventID       string
	GCXEventKind     string
	GCXEventState    string
	GCXEventURL      string
	GCXTeamID        string
	GCXIntegrationID string
	GCXStartedAt     string
}

func (v TriggerVars) toMap() map[string]string {
	return map[string]string{
		"name":               v.Name,
		"date":               v.Date,
		"datetime":           v.Datetime,
		"fire_time":          v.FireTime,
		"session_name":       v.SessionName,
		"worktree_path":      v.WorktreePath,
		"changed_files":      v.ChangedFiles,
		"change_count":       v.ChangeCount,
		"scenario_id":        v.ScenarioID,
		"scenario_name":      v.ScenarioName,
		"completion_epoch":   v.CompletionEpoch,
		"issue_number":       v.IssueNumber,
		"issue_title":        v.IssueTitle,
		"issue_body":         v.IssueBody,
		"issue_url":          v.IssueURL,
		"issue_labels":       v.IssueLabels,
		"gcx_event_id":       v.GCXEventID,
		"gcx_event_kind":     v.GCXEventKind,
		"gcx_event_state":    v.GCXEventState,
		"gcx_event_url":      v.GCXEventURL,
		"gcx_team_id":        v.GCXTeamID,
		"gcx_integration_id": v.GCXIntegrationID,
		"gcx_started_at":     v.GCXStartedAt,
	}
}

// ExpandTrigger replaces {token} occurrences in s using the trigger variable
// set. An unknown token is an error (parity with Expand's discipline).
func ExpandTrigger(s string, vars TriggerVars) (string, error) {
	m := vars.toMap()

	var expandErr error

	out := varPattern.ReplaceAllStringFunc(s, func(match string) string {
		key := match[1 : len(match)-1] // strip { }

		val, ok := m[key]
		if !ok {
			if expandErr == nil {
				expandErr = fmt.Errorf("unknown trigger template variable %q in %q", key, s)
			}

			return match
		}

		return val
	})

	if expandErr != nil {
		return "", expandErr
	}

	return out, nil
}
