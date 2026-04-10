package config

import (
	"fmt"
	"regexp"
)

var varPattern = regexp.MustCompile(`\{(\w+)\}`)

type TemplateVars struct {
	Username       string
	AgentSessionID string
	SessionName    string
	SessionID      string
	WorktreePath   string
}

func (v TemplateVars) toMap() map[string]string {
	return map[string]string{
		"username":         v.Username,
		"agent_session_id": v.AgentSessionID,
		"session_name":     v.SessionName,
		"session_id":       v.SessionID,
		"worktree_path":    v.WorktreePath,
	}
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
