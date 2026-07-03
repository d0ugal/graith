package localmost

import (
	"fmt"
	"strings"
)

// compiledRule is a rule with its DSL compiled to terms.
type compiledRule struct {
	raw      string
	terms    []term
	unless   [][]term
	redirect redirectPolicy
	pipe     pipePolicy
}

// Engine evaluates commands against compiled allow/deny rules.
type Engine struct {
	allow             []compiledRule
	deny              []compiledRule
	allowSafeXargs    bool
	askNoninteractive bool
}

func newEngine(fc fileConfig) (*Engine, error) {
	e := &Engine{allowSafeXargs: true, askNoninteractive: true}

	if fc.AllowSafeXargs != nil {
		e.allowSafeXargs = *fc.AllowSafeXargs
	}

	if fc.AskNoninteractive != nil {
		e.askNoninteractive = *fc.AskNoninteractive
	}

	var err error
	if e.allow, err = compileRules(fc.Allow); err != nil {
		return nil, fmt.Errorf("allow: %w", err)
	}

	if e.deny, err = compileRules(fc.Deny); err != nil {
		return nil, fmt.Errorf("deny: %w", err)
	}

	return e, nil
}

// AskNoninteractive reports the config's askNoninteractive setting (default
// true). When false, callers should turn an "ask" into a "deny" in
// non-interactive contexts.
func (e *Engine) AskNoninteractive() bool { return e.askNoninteractive }

func compileRules(rs []Rule) ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(rs))

	for _, r := range rs {
		if strings.TrimSpace(r.Rule) == "" {
			return nil, fmt.Errorf("empty rule")
		}

		terms, err := compileRule(r.Rule)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.Rule, err)
		}

		var unless [][]term

		for _, u := range r.Unless {
			ut, uerr := compileRule(u)
			if uerr != nil {
				return nil, fmt.Errorf("unless %q: %w", u, uerr)
			}

			unless = append(unless, ut)
		}

		out = append(out, compiledRule{
			raw:      r.Rule,
			terms:    terms,
			unless:   unless,
			redirect: r.Redirect,
			pipe:     r.Pipe,
		})
	}

	return out, nil
}

// Evaluate parses a shell command into subcommands and returns the combined
// policy: deny if any subcommand is denied, allow if all are allowed, otherwise
// ask. A parse failure is treated as ask (fail-safe to the human).
func (e *Engine) Evaluate(command string) (Policy, error) {
	subs, err := parseCommand(command)
	if err != nil {
		return PolicyAsk, err
	}

	if len(subs) == 0 {
		return PolicyAsk, nil
	}

	if e.allowSafeXargs {
		if pol, ok := e.evalXargs(subs); ok {
			return pol, nil
		}
	}

	result := PolicyAllow

	for _, sub := range subs {
		switch e.subPolicy(sub) {
		case PolicyDeny:
			return PolicyDeny, nil
		case PolicyAsk:
			result = PolicyAsk
		}
	}

	return result, nil
}

// subPolicy returns the policy for one subcommand: deny if any deny rule
// matches, else allow if any allow rule matches, else ask.
func (e *Engine) subPolicy(sub subcommand) Policy {
	for _, r := range e.deny {
		if e.ruleMatches(r, sub) {
			return PolicyDeny
		}
	}

	for _, r := range e.allow {
		if e.ruleMatches(r, sub) {
			return PolicyAllow
		}
	}

	return PolicyAsk
}

// tokensAllowed builds a standalone subcommand from tokens and reports whether
// it resolves to allow (used by @sub and the xargs helper).
func (e *Engine) tokensAllowed(tokens []token) bool {
	return e.subPolicy(subcommand{tokens: tokens}) == PolicyAllow
}

// evalXargs implements the allowSafeXargs special cases for a two-command
// pipeline "LEFT | xargs PROG ARGS":
//
//   - echo ARGS | xargs PROG  → allow iff "PROG ARGS" would be allowed
//   - PROG1 ARGS1 | xargs PROG2 ARGS2 → allow iff LEFT is allowed and a
//     "PROG2 ARGS2 @arg*"-equivalent allow rule exists.
//
// It returns (policy, true) only when it can positively allow; otherwise
// (_, false) to fall through to normal evaluation. This is a best-effort,
// documented-partial implementation (xargs's own options are not stripped from
// PROG2).
func (e *Engine) evalXargs(subs []subcommand) (Policy, bool) {
	if len(subs) != 2 {
		return "", false
	}

	left, right := subs[0], subs[1]
	if !left.pipeStart || !right.pipeEnd || right.name() != "xargs" {
		return "", false
	}

	if e.subPolicy(left) != PolicyAllow {
		return "", false
	}

	prog2 := wordTokensAfter(right, "xargs")
	if len(prog2) == 0 {
		return "", false
	}

	if left.name() == "echo" {
		combined := append(append([]token{}, prog2...), argTokensAfterName(left)...)
		if e.tokensAllowed(combined) {
			return PolicyAllow, true
		}

		return "", false
	}

	if e.tokensAllowed(prog2) {
		return PolicyAllow, true
	}

	return "", false
}

// wordTokensAfter returns the word tokens that follow the first token whose
// text equals name.
func wordTokensAfter(sub subcommand, name string) []token {
	found := false

	var out []token

	for _, t := range sub.tokens {
		if found {
			if t.kind == tokWord {
				out = append(out, t)
			}

			continue
		}

		if t.kind == tokWord && t.text == name {
			found = true
		}
	}

	return out
}

// argTokensAfterName returns the argument tokens of a subcommand (its word
// tokens after the command name).
func argTokensAfterName(sub subcommand) []token {
	seenName := false

	var out []token

	for _, t := range sub.tokens {
		if t.kind != tokWord {
			continue
		}

		if !seenName {
			seenName = true
			continue
		}

		out = append(out, t)
	}

	return out
}
