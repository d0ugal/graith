package localmost

import (
	"fmt"
	"strings"
)

// termKind identifies a compiled rule term.
type termKind int

const (
	termLiteral termKind = iota // an exact word, e.g. "git", "-C"
	termArg                     // @arg  — any single literal argument
	termPath                    // @path — a literal argument that is a valid path
	termInt                     // @int  — a literal integer argument
	termEnv                     // @env  — a FOO=bar assignment
	termSub                     // @sub  — trailing subcommand that must be allow
	termChoice                  // @{a,b,c}
	termGroup                   // @(a b c)
)

// quant is a term quantifier.
type quant int

const (
	quantOne  quant = iota // exactly one (no quantifier)
	quantOpt               // ?  zero or one
	quantStar              // *  zero or more
	quantPlus              // +  one or more
)

// term is one compiled element of a rule.
type term struct {
	kind    termKind
	literal string
	choices []string
	group   []term
	quant   quant
}

// compileRule parses a localmost rule string into a term sequence.
func compileRule(s string) ([]term, error) {
	toks, err := splitRuleTokens(s)
	if err != nil {
		return nil, err
	}

	terms := make([]term, 0, len(toks))

	for i, tok := range toks {
		tm, err := compileToken(tok)
		if err != nil {
			return nil, err
		}

		if tm.kind == termSub {
			if i != len(toks)-1 {
				return nil, fmt.Errorf("@sub must be the last expression in a rule")
			}

			if tm.quant != quantOne {
				return nil, fmt.Errorf("@sub does not accept a quantifier")
			}
		}

		terms = append(terms, tm)
	}

	return terms, nil
}

// splitRuleTokens splits a rule string into whitespace-separated tokens while
// keeping @{...} and @(...) groups (which may contain spaces/commas) intact.
func splitRuleTokens(s string) ([]string, error) {
	var (
		toks []string
		cur  strings.Builder
	)

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]

		if c == ' ' || c == '\t' || c == '\n' {
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}

			continue
		}

		// A group opener: @( ... ) or @{ ... }. Consume the balanced span,
		// including a trailing quantifier, as a single token.
		if c == '@' && i+1 < len(runes) && (runes[i+1] == '(' || runes[i+1] == '{') {
			span, next, err := consumeGroup(runes, i)
			if err != nil {
				return nil, err
			}

			cur.WriteString(span)

			i = next - 1

			continue
		}

		cur.WriteRune(c)
	}

	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}

	return toks, nil
}

// consumeGroup reads a balanced @(...) or @{...} span starting at runes[i]
// (runes[i]=='@'), plus any trailing quantifier, and returns the span text and
// the index just past it.
func consumeGroup(runes []rune, i int) (string, int, error) {
	open := runes[i+1]

	var closeR rune

	switch open {
	case '(':
		closeR = ')'
	case '{':
		closeR = '}'
	}

	depth := 0
	j := i + 1

	for ; j < len(runes); j++ {
		switch runes[j] {
		case open:
			depth++
		case closeR:
			depth--
			if depth == 0 {
				end := j + 1
				// Include a trailing quantifier if present.
				if end < len(runes) && isQuantRune(runes[end]) {
					end++
				}

				return string(runes[i:end]), end, nil
			}
		}
	}

	return "", 0, fmt.Errorf("unbalanced %q in rule", string(open))
}

func isQuantRune(r rune) bool { return r == '?' || r == '+' || r == '*' }

// compileToken compiles a single rule token into a term.
func compileToken(tok string) (term, error) {
	// Strip a trailing quantifier (except for group/choice tokens, whose
	// quantifier is handled after parsing the body).
	body, q := tok, quantOne

	if !strings.HasPrefix(tok, "@(") && !strings.HasPrefix(tok, "@{") {
		if n := len(tok); n >= 2 && isQuantRune(rune(tok[n-1])) {
			body, q = tok[:n-1], quantOf(rune(tok[n-1]))
		}
	}

	// Meta expressions.
	switch {
	case tok == "@*":
		return term{kind: termArg, quant: quantStar}, nil
	case body == "@arg":
		return term{kind: termArg, quant: q}, nil
	case body == "@path":
		return term{kind: termPath, quant: q}, nil
	case body == "@int":
		return term{kind: termInt, quant: q}, nil
	case body == "@env":
		return term{kind: termEnv, quant: q}, nil
	case body == "@sub":
		return term{kind: termSub}, nil
	case body == "@@":
		return term{kind: termLiteral, literal: "@", quant: q}, nil
	case strings.HasPrefix(tok, "@{"):
		return compileChoice(tok)
	case strings.HasPrefix(tok, "@("):
		return compileGroup(tok)
	case strings.HasPrefix(body, "@"):
		return term{}, fmt.Errorf("unknown meta expression %q", tok)
	default:
		return term{kind: termLiteral, literal: body, quant: q}, nil
	}
}

func quantOf(r rune) quant {
	switch r {
	case '?':
		return quantOpt
	case '+':
		return quantPlus
	case '*':
		return quantStar
	default:
		return quantOne
	}
}

// compileChoice compiles @{a,b,c} with an optional trailing quantifier.
func compileChoice(tok string) (term, error) {
	inner, q, err := groupBody(tok, '{', '}')
	if err != nil {
		return term{}, err
	}

	parts := strings.Split(inner, ",")
	choices := make([]string, 0, len(parts))

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		choices = append(choices, p)
	}

	if len(choices) == 0 {
		return term{}, fmt.Errorf("empty choice %q", tok)
	}

	return term{kind: termChoice, choices: choices, quant: q}, nil
}

// compileGroup compiles @(a b c) (an ordered group of terms) with an optional
// trailing quantifier.
func compileGroup(tok string) (term, error) {
	inner, q, err := groupBody(tok, '(', ')')
	if err != nil {
		return term{}, err
	}

	sub, err := compileRule(inner)
	if err != nil {
		return term{}, fmt.Errorf("in group %q: %w", tok, err)
	}

	return term{kind: termGroup, group: sub, quant: q}, nil
}

// groupBody strips the @<open> ... <closeR> wrapper and a trailing quantifier,
// returning the inner text and the quantifier.
func groupBody(tok string, open, closeR rune) (string, quant, error) {
	q := quantOne
	end := len(tok)

	if n := len(tok); n >= 1 && isQuantRune(rune(tok[n-1])) {
		q = quantOf(rune(tok[n-1]))
		end = n - 1
	}

	if len(tok) < 3 || rune(tok[1]) != open || rune(tok[end-1]) != closeR {
		return "", quantOne, fmt.Errorf("malformed group %q", tok)
	}

	return tok[2 : end-1], q, nil
}
