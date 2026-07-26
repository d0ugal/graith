// Package cigate owns the repository-side contract for the graith-ci-gate
// GitHub App evaluator.
//
// The package deliberately separates validations that can be proven from a
// hermetic bundle from validations that require a disposable live GitHub
// fixture. Local evaluation can prove policy, plan, result, provenance, and
// replay consistency only when the caller supplies evaluator-owned trust anchors
// for policy, config, and evaluator digests, and derives the event context from
// the signed webhook body. It cannot prove GitHub App source restriction,
// ruleset source binding, merge queue dispatch, or GitHub permission behavior.
package cigate
