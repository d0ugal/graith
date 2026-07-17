package scenariofile

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	CompletionAll    = "all"
	CompletionQuorum = "quorum"

	OnExhaustedWait = "wait"
	OnExhaustedFail = "fail"

	PolicyResolution = time.Second
	MaxRetries       = 10
)

// PolicyConfig is the optional [scenario.policy] TOML block.
type PolicyConfig struct {
	Completion  string `toml:"completion"`
	Quorum      int    `toml:"quorum"`
	OnExhausted string `toml:"on_exhausted"`
}

// MemberPolicyConfig is the optional [sessions.policy] TOML block.
type MemberPolicyConfig struct {
	Required *bool  `toml:"required"`
	Timeout  string `toml:"timeout"`
	Retries  int    `toml:"retries"`
}

// PolicyMember is the policy-relevant subset of a scenario member.
type PolicyMember struct {
	Name              string
	Task              string
	HasRequiredResult bool
	Shared            bool
	Policy            *MemberPolicyConfig
}

// NormalizedPolicy contains defaults resolved for authoritative daemon state.
type NormalizedPolicy struct {
	Completion  string
	Quorum      int
	OnExhausted string
	Members     []NormalizedMemberPolicy
}

// NormalizedMemberPolicy is one member's resolved policy.
type NormalizedMemberPolicy struct {
	Required bool
	Timeout  time.Duration
	Retries  int
}

// NormalizePolicy validates and resolves scenario runtime policy. A nil result
// means neither the scenario nor any member opted into policy semantics.
func NormalizePolicy(policy *PolicyConfig, members []PolicyMember) (*NormalizedPolicy, error) {
	enabled := policy != nil
	for _, member := range members {
		enabled = enabled || member.Policy != nil
	}

	if !enabled {
		return nil, nil
	}

	result := &NormalizedPolicy{
		Completion:  CompletionAll,
		OnExhausted: OnExhaustedWait,
		Members:     make([]NormalizedMemberPolicy, len(members)),
	}
	if policy != nil {
		if policy.Completion != "" {
			result.Completion = strings.ToLower(policy.Completion)
		}

		result.Quorum = policy.Quorum
		if policy.OnExhausted != "" {
			result.OnExhausted = strings.ToLower(policy.OnExhausted)
		}
	}

	switch result.Completion {
	case CompletionAll, CompletionQuorum:
	default:
		return nil, fmt.Errorf("scenario.policy.completion must be %q or %q (got %q)", CompletionAll, CompletionQuorum, result.Completion)
	}

	switch result.OnExhausted {
	case OnExhaustedWait, OnExhaustedFail:
	default:
		return nil, fmt.Errorf("scenario.policy.on_exhausted must be %q or %q (got %q)", OnExhaustedWait, OnExhaustedFail, result.OnExhausted)
	}

	requiredCount := 0

	for i, member := range members {
		resolved := NormalizedMemberPolicy{Required: true}
		if member.Policy != nil {
			if member.Policy.Required != nil {
				resolved.Required = *member.Policy.Required
			}

			resolved.Retries = member.Policy.Retries
			if resolved.Retries < 0 {
				return nil, fmt.Errorf("session %q: policy.retries must not be negative", member.Name)
			}

			if resolved.Retries > MaxRetries {
				return nil, fmt.Errorf("session %q: policy.retries must be at most %d", member.Name, MaxRetries)
			}

			if member.Policy.Timeout != "" {
				d, err := time.ParseDuration(member.Policy.Timeout)
				if err != nil {
					return nil, fmt.Errorf("session %q: parse policy.timeout: %w", member.Name, err)
				}

				if d <= 0 {
					return nil, fmt.Errorf("session %q: policy.timeout must be positive", member.Name)
				}

				if d < PolicyResolution {
					return nil, fmt.Errorf("session %q: policy.timeout must be at least %s", member.Name, PolicyResolution)
				}

				resolved.Timeout = d
			}
		}

		if resolved.Retries > 0 && resolved.Timeout == 0 {
			return nil, fmt.Errorf("session %q: policy.retries requires policy.timeout", member.Name)
		}

		if member.Shared && (resolved.Timeout > 0 || resolved.Retries > 0) {
			return nil, fmt.Errorf("session %q: shared members cannot have timeout or retry policy", member.Name)
		}

		if resolved.Required {
			requiredCount++
		}

		result.Members[i] = resolved
	}

	switch result.Completion {
	case CompletionAll:
		if result.Quorum != 0 {
			return nil, errors.New("scenario.policy.quorum is only valid when completion = \"quorum\"")
		}

		if requiredCount == 0 {
			return nil, errors.New("completion = \"all\" requires at least one required member")
		}

	case CompletionQuorum:
		if result.Quorum <= 0 {
			return nil, errors.New("scenario.policy.quorum must be positive when completion = \"quorum\"")
		}

		if result.Quorum > len(members) {
			return nil, fmt.Errorf("scenario.policy.quorum %d exceeds member count %d", result.Quorum, len(members))
		}

		if result.Quorum < requiredCount {
			return nil, fmt.Errorf("scenario.policy.quorum %d is lower than the %d required members", result.Quorum, requiredCount)
		}
	}

	return result, nil
}

// ValidatePolicyContracts verifies that every policy-managed member has a
// seedable todo result contract. maxTitle is the effective todo title limit;
// pass zero to check only presence.
func ValidatePolicyContracts(policy *NormalizedPolicy, members []PolicyMember, maxTitle int) error {
	if policy == nil {
		return nil
	}

	for _, member := range members {
		task := strings.TrimSpace(member.Task)
		if task == "" && !member.HasRequiredResult {
			return fmt.Errorf("session %q: runtime policy requires a non-empty task or required result contract", member.Name)
		}

		if task != "" && maxTitle > 0 && len(task) > maxTitle {
			return fmt.Errorf("session %q: task result contract exceeds todo title limit %d", member.Name, maxTitle)
		}
	}

	return nil
}
