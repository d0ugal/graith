// Package localmost is a clean-room, behaviourally-compatible reimplementation
// of the federicotdn/localmost rule engine. It evaluates a shell command
// against allow/deny rules loaded from a localmost-format config.json and
// returns one of three policies: allow, ask, or deny.
//
// It is written from localmost's public documentation (its README and
// docs/examples.md), NOT from its GPL-3.0 Haskell source. Behavioural parity is
// validated by a conformance corpus rather than bit-identical parsing (graith
// uses mvdan.cc/sh where localmost uses ShellCheck). Known divergences are
// tracked in docs/design/2026-07-03-pluggable-approvals-backends-design.md.
package localmost

import (
	"encoding/json"
	"fmt"
	"os"
)

// Policy is the result of evaluating a command.
type Policy string

const (
	PolicyAllow Policy = "allow"
	PolicyAsk   Policy = "ask"
	PolicyDeny  Policy = "deny"
)

// redirectPolicy is the per-rule redirect constraint.
type redirectPolicy int

const (
	redirectSafe redirectPolicy = iota // default: only non-destructive redirects
	redirectAny                        // true: any redirects
	redirectNone                       // false: no redirects
)

// pipePolicy is the per-rule pipeline-position constraint.
type pipePolicy int

const (
	pipeAny  pipePolicy = iota // default (true): match regardless of pipeline
	pipeNone                   // false: only when not in a pipeline
	pipeIn                     // "in": standalone or at the end of a pipeline
	pipeOut                    // "out": standalone or at the start of a pipeline
)

// Rule is a single allow/deny rule from the config.
type Rule struct {
	Rule     string
	Unless   []string
	Redirect redirectPolicy
	Pipe     pipePolicy
}

// UnmarshalJSON parses a rule object, applying localmost's defaults
// (redirect="safe", pipe=true) and accepting the bool/string forms of the
// redirect and pipe keys.
func (r *Rule) UnmarshalJSON(b []byte) error {
	var raw struct {
		Rule     string          `json:"rule"`
		Unless   []string        `json:"unless"`
		Redirect json.RawMessage `json:"redirect"`
		Pipe     json.RawMessage `json:"pipe"`
	}

	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	r.Rule = raw.Rule
	r.Unless = raw.Unless

	r.Redirect = redirectSafe
	if len(raw.Redirect) > 0 {
		p, err := parseRedirect(raw.Redirect)
		if err != nil {
			return err
		}

		r.Redirect = p
	}

	r.Pipe = pipeAny
	if len(raw.Pipe) > 0 {
		p, err := parsePipe(raw.Pipe)
		if err != nil {
			return err
		}

		r.Pipe = p
	}

	return nil
}

func parseRedirect(raw json.RawMessage) (redirectPolicy, error) {
	var bv bool
	if json.Unmarshal(raw, &bv) == nil {
		if bv {
			return redirectAny, nil
		}

		return redirectNone, nil
	}

	var sv string
	if json.Unmarshal(raw, &sv) == nil && sv == "safe" {
		return redirectSafe, nil
	}

	return redirectSafe, fmt.Errorf("redirect must be true, false, or \"safe\" (got %s)", raw)
}

func parsePipe(raw json.RawMessage) (pipePolicy, error) {
	var bv bool
	if json.Unmarshal(raw, &bv) == nil {
		if bv {
			return pipeAny, nil
		}

		return pipeNone, nil
	}

	var sv string
	if json.Unmarshal(raw, &sv) == nil {
		switch sv {
		case "in":
			return pipeIn, nil
		case "out":
			return pipeOut, nil
		}
	}

	return pipeAny, fmt.Errorf(`pipe must be true, false, "in", or "out" (got %s)`, raw)
}

// fileConfig is the on-disk config.json shape.
type fileConfig struct {
	Allow             []Rule `json:"allow"`
	Deny              []Rule `json:"deny"`
	AllowSafeXargs    *bool  `json:"allowSafeXargs"`
	AskNoninteractive *bool  `json:"askNoninteractive"`
}

// Load reads and compiles a localmost-format config.json from path.
func Load(path string) (*Engine, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is operator-configured
	if err != nil {
		return nil, fmt.Errorf("read approvals config: %w", err)
	}

	return Parse(data)
}

// Parse compiles a localmost-format config from raw JSON bytes.
func Parse(data []byte) (*Engine, error) {
	var fc fileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parse approvals config: %w", err)
	}

	return newEngine(fc)
}
