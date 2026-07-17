package scenariofile

import (
	"strings"
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }

func TestNormalizePolicyLegacyDisabled(t *testing.T) {
	got, err := NormalizePolicy(nil, []PolicyMember{{Name: "braw"}})
	if err != nil || got != nil {
		t.Fatalf("NormalizePolicy() = %+v, %v; want nil, nil", got, err)
	}
}

func TestNormalizePolicyDefaultsAndQuorum(t *testing.T) {
	policy, err := NormalizePolicy(&PolicyConfig{Completion: CompletionQuorum, Quorum: 2}, []PolicyMember{
		{Name: "braw", Policy: &MemberPolicyConfig{Timeout: "30s", Retries: 2}},
		{Name: "canny", Policy: &MemberPolicyConfig{Required: boolPtr(false), Timeout: "1m"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if policy.Completion != CompletionQuorum || policy.Quorum != 2 || policy.OnExhausted != OnExhaustedWait {
		t.Errorf("normalized scenario policy = %+v", policy)
	}

	if !policy.Members[0].Required || policy.Members[0].Timeout != 30*time.Second || policy.Members[0].Retries != 2 {
		t.Errorf("required member = %+v", policy.Members[0])
	}

	if policy.Members[1].Required {
		t.Errorf("optional member normalized as required: %+v", policy.Members[1])
	}
}

func TestNormalizePolicyRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name    string
		policy  *PolicyConfig
		members []PolicyMember
		want    string
	}{
		{"unknown completion", &PolicyConfig{Completion: "first"}, []PolicyMember{{Name: "braw"}}, "completion"},
		{"unknown exhaustion", &PolicyConfig{OnExhausted: "ignore"}, []PolicyMember{{Name: "braw"}}, "on_exhausted"},
		{"zero quorum", &PolicyConfig{Completion: CompletionQuorum}, []PolicyMember{{Name: "braw"}}, "must be positive"},
		{"excessive quorum", &PolicyConfig{Completion: CompletionQuorum, Quorum: 2}, []PolicyMember{{Name: "braw"}}, "exceeds member count"},
		{"quorum below required", &PolicyConfig{Completion: CompletionQuorum, Quorum: 1}, []PolicyMember{{Name: "braw"}, {Name: "canny"}}, "lower than"},
		{"quorum with all", &PolicyConfig{Completion: CompletionAll, Quorum: 1}, []PolicyMember{{Name: "braw"}}, "only valid"},
		{"all optional", &PolicyConfig{}, []PolicyMember{{Name: "braw", Policy: &MemberPolicyConfig{Required: boolPtr(false)}}}, "at least one required"},
		{"negative retries", &PolicyConfig{}, []PolicyMember{{Name: "braw", Policy: &MemberPolicyConfig{Retries: -1}}}, "must not be negative"},
		{"unbounded retries", &PolicyConfig{}, []PolicyMember{{Name: "braw", Policy: &MemberPolicyConfig{Timeout: "1m", Retries: MaxRetries + 1}}}, "at most"},
		{"retries without timeout", &PolicyConfig{}, []PolicyMember{{Name: "braw", Policy: &MemberPolicyConfig{Retries: 1}}}, "requires policy.timeout"},
		{"zero timeout", &PolicyConfig{}, []PolicyMember{{Name: "braw", Policy: &MemberPolicyConfig{Timeout: "0s"}}}, "must be positive"},
		{"negative timeout", &PolicyConfig{}, []PolicyMember{{Name: "braw", Policy: &MemberPolicyConfig{Timeout: "-1s"}}}, "must be positive"},
		{"sub-resolution timeout", &PolicyConfig{}, []PolicyMember{{Name: "braw", Policy: &MemberPolicyConfig{Timeout: "999ms"}}}, "at least 1s"},
		{"bad timeout", &PolicyConfig{}, []PolicyMember{{Name: "braw", Policy: &MemberPolicyConfig{Timeout: "dreich"}}}, "parse policy.timeout"},
		{"shared timeout", &PolicyConfig{}, []PolicyMember{{Name: "braw", Shared: true, Policy: &MemberPolicyConfig{Timeout: "1m"}}}, "shared members"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizePolicy(tt.policy, tt.members)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidatePolicyContracts(t *testing.T) {
	policy := &NormalizedPolicy{}

	for _, tt := range []struct {
		name     string
		members  []PolicyMember
		maxTitle int
		want     string
	}{
		{name: "missing task", members: []PolicyMember{{Name: "braw"}}, maxTitle: 20, want: "non-empty task"},
		{name: "oversize task", members: []PolicyMember{{Name: "canny", Task: "dreich"}}, maxTitle: 5, want: "title limit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePolicyContracts(policy, tt.members, tt.maxTitle)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}

	if err := ValidatePolicyContracts(policy, []PolicyMember{{Name: "bothy", Task: "review"}}, 20); err != nil {
		t.Fatalf("valid contract: %v", err)
	}

	if err := ValidatePolicyContracts(policy, []PolicyMember{{Name: "croft", HasRequiredResult: true}}, 20); err != nil {
		t.Fatalf("valid required-result-only contract: %v", err)
	}

	if err := ValidatePolicyContracts(nil, []PolicyMember{{Name: "legacy"}}, 1); err != nil {
		t.Fatalf("legacy contract validation changed behavior: %v", err)
	}
}
