package daemon

import (
	"strings"
	"testing"
)

func TestValidateSessionName(t *testing.T) {
	valid := []string{
		"my-session",
		"fix-bug-123",
		"feature_branch",
		"a",
		"A",
		"session.name",
		"my-session.v2",
		"123-numeric-start",
		"ALL-CAPS",
		"MixedCase",
		strings.Repeat("a", 128),
	}
	for _, name := range valid {
		if err := ValidateSessionName(name); err != nil {
			t.Errorf("ValidateSessionName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []struct {
		name    string
		wantSub string
	}{
		{"", "must not be empty"},
		{"has space", "invalid"},
		{"has\nnewline", "invalid"},
		{"has\ttab", "invalid"},
		{"-leading-dash", "must start with an alphanumeric"},
		{"_leading-underscore", "must start with an alphanumeric"},
		{".leading-dot", "must start with an alphanumeric"},
		{"semi;colon", "invalid"},
		{"pipe|char", "invalid"},
		{"amp&ersand", "invalid"},
		{"dollar$sign", "invalid"},
		{"back`tick", "invalid"},
		{"single'quote", "invalid"},
		{"double\"quote", "invalid"},
		{"path/separator", "invalid"},
		{"back\\slash", "invalid"},
		{"paren(open", "invalid"},
		{"paren)close", "invalid"},
		{"curly{brace", "invalid"},
		{"curly}brace", "invalid"},
		{"angle<bracket", "invalid"},
		{"angle>bracket", "invalid"},
		{"star*glob", "invalid"},
		{"question?mark", "invalid"},
		{"hash#tag", "invalid"},
		{"exclam!", "invalid"},
		{"at@sign", "invalid"},
		{"percent%sign", "invalid"},
		{"caret^char", "invalid"},
		{"tilde~char", "invalid"},
		{"equal=sign", "invalid"},
		{"plus+sign", "invalid"},
		{"comma,sep", "invalid"},
		{"colon:sep", "invalid"},
		{"bracket[open", "invalid"},
		{"bracket]close", "invalid"},
		{"parent..traversal", "must not contain \"..\""},
		{"trailing-dot..", "must not contain \"..\""},
		{"..leading", "must not contain \"..\""},
		{"\x00null", "invalid"},
		{"\x1besc", "invalid"},
		{strings.Repeat("a", 129), "128 characters or fewer"},
	}
	for _, tc := range invalid {
		err := ValidateSessionName(tc.name)
		if err == nil {
			t.Errorf("ValidateSessionName(%q) = nil, want error containing %q", tc.name, tc.wantSub)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("ValidateSessionName(%q) = %q, want error containing %q", tc.name, err.Error(), tc.wantSub)
		}
	}
}
