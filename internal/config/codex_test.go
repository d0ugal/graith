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
