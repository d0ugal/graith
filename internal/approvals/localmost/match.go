package localmost

// matchState carries the tokens being matched plus the engine, for @sub
// recursion.
type matchState struct {
	eng    *Engine
	tokens []token
}

// ruleMatches reports whether a compiled rule matches a subcommand: the term
// sequence must consume all tokens, the redirect/pipe constraints must hold,
// and no unless expression may appear anywhere in the tokens.
func (e *Engine) ruleMatches(r compiledRule, sub subcommand) bool {
	if !redirectOK(r.redirect, sub) {
		return false
	}

	if !pipeOK(r.pipe, sub) {
		return false
	}

	ms := &matchState{eng: e, tokens: sub.tokens}

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
	if len(terms) == 0 {
		return []int{pos}
	}

	t := terms[0]
	rest := terms[1:]

	// @sub is terminal: it consumes the rest of the tokens as a subcommand that
	// must itself resolve to allow.
	if t.kind == termSub {
		if ms.eng.tokensAllowed(ms.tokens[pos:]) {
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
		return tok.kind == tokAssign
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
