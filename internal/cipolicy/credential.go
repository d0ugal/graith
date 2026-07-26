package cipolicy

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	syntheticReadToken            = "read"
	syntheticRepositoryWriteToken = "repository-write"
	syntheticMaintainerToken      = "maintainer"
)

type SyntheticToken struct {
	Name         string
	TrustTier    string
	Class        string
	Scopes       []string
	AllowedRoots []string
}

type CredentialOperation struct {
	Operation  string
	TrustTier  string
	Capability string
	Token      SyntheticToken
	Target     string
}

type credentialOperationPolicy struct {
	TokenClasses   []string
	TrustTiers     []string
	Capabilities   []string
	RequiredScopes []string
	AllowedRoots   []string
}

var credentialOperationPolicies = map[string]credentialOperationPolicy{
	"docs-preview-write": {
		TokenClasses:   []string{syntheticRepositoryWriteToken},
		TrustTiers:     []string{"same-repository-agent"},
		Capabilities:   []string{"docs-preview"},
		RequiredScopes: []string{"contents:write", "pull-requests:write"},
		AllowedRoots:   []string{"screenshots"},
	},
	"coverage-comment": {
		TokenClasses:   []string{syntheticRepositoryWriteToken},
		TrustTiers:     []string{"same-repository-agent"},
		Capabilities:   []string{"coverage"},
		RequiredScopes: []string{"pull-requests:write"},
		AllowedRoots:   []string{"comments"},
	},
	"regeneration-push": {
		TokenClasses:   []string{syntheticMaintainerToken},
		TrustTiers:     []string{"trusted-publication"},
		Capabilities:   []string{"generated-metadata"},
		RequiredScopes: []string{"contents:write"},
		AllowedRoots:   []string{"generated"},
	},
	"dev-release-publish": {
		TokenClasses:   []string{syntheticMaintainerToken},
		TrustTiers:     []string{"trusted-publication"},
		Capabilities:   []string{"dev-release"},
		RequiredScopes: []string{"contents:write"},
		AllowedRoots:   []string{"dist/dev-release"},
	},
	"stable-release-publish": {
		TokenClasses:   []string{syntheticMaintainerToken},
		TrustTiers:     []string{"trusted-publication"},
		Capabilities:   []string{"stable-release"},
		RequiredScopes: []string{"contents:write"},
		AllowedRoots:   []string{"dist/stable-release"},
	},
}

func ValidateCredentialOperation(operation CredentialOperation) error {
	policy, ok := credentialOperationPolicies[operation.Operation]
	if !ok {
		return fmt.Errorf("unsupported credential operation %q", operation.Operation)
	}

	if operation.Token.Name == "" {
		return errors.New("synthetic token identity is required")
	}

	if operation.Token.TrustTier != operation.TrustTier {
		return fmt.Errorf("synthetic token trust tier %s does not match operation trust tier %s", operation.Token.TrustTier, operation.TrustTier)
	}

	if operation.TrustTier == "fork-untrusted" && operation.Token.Class != syntheticReadToken {
		return errors.New("fork pull requests may use only synthetic read tokens")
	}

	if operation.TrustTier == "same-repository-agent" && operation.Token.Class == syntheticMaintainerToken {
		return errors.New("same-repository agent branches cannot obtain maintainer credentials from repository location")
	}

	if !containsString(policy.TrustTiers, operation.TrustTier) {
		return fmt.Errorf("%s is not allowed for trust tier %s", operation.Operation, operation.TrustTier)
	}

	if !containsString(policy.TokenClasses, operation.Token.Class) {
		if containsString(policy.TokenClasses, syntheticMaintainerToken) {
			return fmt.Errorf("%s requires a maintainer credential", operation.Operation)
		}

		if operation.Token.Class == syntheticMaintainerToken {
			return fmt.Errorf("%s must not use maintainer credentials", operation.Operation)
		}

		return fmt.Errorf("%s cannot use synthetic %s tokens", operation.Operation, operation.Token.Class)
	}

	for _, scope := range policy.RequiredScopes {
		if !containsString(operation.Token.Scopes, scope) {
			return fmt.Errorf("%s token is missing required scope %s", operation.Operation, scope)
		}
	}

	for _, scope := range operation.Token.Scopes {
		if !containsString(policy.RequiredScopes, scope) {
			return fmt.Errorf("%s token has unsupported scope %s", operation.Operation, scope)
		}
	}

	for _, root := range operation.Token.AllowedRoots {
		if !targetWithinAllowedRoots(root, policy.AllowedRoots) {
			return fmt.Errorf("%s token root %q is outside the operation boundary", operation.Operation, root)
		}
	}

	if !targetWithinAllowedRoots(operation.Target, operation.Token.AllowedRoots) {
		return fmt.Errorf("%s target %q escapes synthetic token filesystem boundary", operation.Operation, operation.Target)
	}

	if !targetWithinAllowedRoots(operation.Target, policy.AllowedRoots) {
		return fmt.Errorf("%s target %q is not allowed for the operation boundary", operation.Operation, operation.Target)
	}

	return nil
}

func validateCredentialOperationPlanBinding(operation CredentialOperation, policy credentialOperationPolicy, plan RunPlan) error {
	if operation.Capability == "" {
		return fmt.Errorf("%s credential operation requires plan capability identity", operation.Operation)
	}

	if !containsString(policy.Capabilities, operation.Capability) {
		return fmt.Errorf("%s credential operation cannot use plan capability %s", operation.Operation, operation.Capability)
	}

	if !containsString(plan.Capabilities, operation.Capability) {
		return fmt.Errorf("%s credential operation requires plan capability %s", operation.Operation, operation.Capability)
	}

	return nil
}

func targetWithinAllowedRoots(target string, roots []string) bool {
	if target == "" || filepath.IsAbs(target) || invalidChangedPath(filepath.ToSlash(target)) {
		return false
	}

	cleanTarget := filepath.ToSlash(filepath.Clean(target))
	for _, root := range roots {
		cleanRoot := filepath.ToSlash(filepath.Clean(root))
		if cleanRoot == "." || invalidChangedPath(cleanRoot) {
			continue
		}

		if cleanTarget == cleanRoot || strings.HasPrefix(cleanTarget, cleanRoot+"/") {
			return true
		}
	}

	return false
}
