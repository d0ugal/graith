package config

import "testing"

func TestCodexOptionsIsZero(t *testing.T) {
	if !(CodexOptions{}).IsZero() {
		t.Fatal("empty CodexOptions should report IsZero")
	}

	cases := []CodexOptions{
		{Profile: "braw"},
		{ReasoningEffort: "high"},
		{ServiceTier: "flex"},
		{WebSearch: true},
		{ApprovalPolicy: "never"},
	}
	for _, c := range cases {
		if c.IsZero() {
			t.Errorf("CodexOptions %+v should not report IsZero", c)
		}
	}
}

func TestValidateCodexOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    CodexOptions
		wantErr bool
	}{
		{name: "empty is valid", opts: CodexOptions{}},
		{name: "all valid", opts: CodexOptions{
			Profile:         "braw",
			ReasoningEffort: "high",
			ServiceTier:     "flex",
			WebSearch:       true,
			ApprovalPolicy:  "on-request",
		}},
		{name: "reasoning minimal", opts: CodexOptions{ReasoningEffort: "minimal"}},
		{name: "reasoning xhigh", opts: CodexOptions{ReasoningEffort: "xhigh"}},
		{name: "approval untrusted", opts: CodexOptions{ApprovalPolicy: "untrusted"}},
		{name: "approval never", opts: CodexOptions{ApprovalPolicy: "never"}},
		{name: "tier priority", opts: CodexOptions{ServiceTier: "priority"}},
		{name: "profile is free-form", opts: CodexOptions{Profile: "onie-that-thrawn-name"}},

		{name: "bad reasoning", opts: CodexOptions{ReasoningEffort: "dreich"}, wantErr: true},
		{name: "bad tier", opts: CodexOptions{ServiceTier: "haar"}, wantErr: true},
		{name: "bad approval", opts: CodexOptions{ApprovalPolicy: "on-failure"}, wantErr: true},
		{name: "approval must be hyphenated", opts: CodexOptions{ApprovalPolicy: "on request"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCodexOptions(tt.opts)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateCodexOptions(%+v) = nil, want error", tt.opts)
			}

			if !tt.wantErr && err != nil {
				t.Errorf("ValidateCodexOptions(%+v) = %v, want nil", tt.opts, err)
			}
		})
	}
}
