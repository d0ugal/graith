package localmost

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// tokenKind classifies a subcommand token.
type tokenKind int

const (
	tokWord   tokenKind = iota // command name or argument
	tokAssign                  // leading FOO=bar assignment
)

// token is one element of a subcommand's match stream.
type token struct {
	text    string
	kind    tokenKind
	literal bool // true if the word has no shell expansions ($var, $(...), etc.)
}

// redir is a redirection attached to a subcommand.
type redir struct {
	safe bool
}

// subcommand is one simple command extracted from an input command, with the
// context needed to evaluate rule constraints.
type subcommand struct {
	tokens    []token
	redirs    []redir
	inPipe    bool
	pipeStart bool
	pipeEnd   bool
}

// name returns the command name (first non-assignment token), or "".
func (s subcommand) name() string {
	for _, t := range s.tokens {
		if t.kind == tokWord {
			return t.text
		}
	}

	return ""
}

// parseCommand parses a shell command string into its subcommands. Parsing
// mirrors localmost's approach (break into simple commands, tracking pipeline
// position and redirects) using mvdan.cc/sh instead of ShellCheck.
func parseCommand(cmd string) ([]subcommand, error) {
	f, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, err
	}

	var subs []subcommand

	c := &collector{out: &subs}
	for _, st := range f.Stmts {
		c.stmt(st, pipeCtx{})
	}

	// Command and process substitutions ($(...), `...`, <(...), >(...)) are
	// executed by the shell but live inside word parts, which the statement
	// walk above never descends into. Harvest their inner commands as
	// standalone subcommands so they are still checked against deny/allow
	// rules — otherwise a dangerous command hidden in a substitution would
	// silently escape evaluation. This covers substitutions anywhere: an
	// assignment value (FOO=$(evil) make), a redirect target (cat < <(evil)),
	// a here-string / heredoc body (cat <<< $(evil)), a for-loop list
	// (for f in $(evil)), a case subject (case $(evil) in), etc. See #782.
	syntax.Walk(f, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.CmdSubst:
			c.stmts(x.Stmts)
		case *syntax.ProcSubst:
			c.stmts(x.Stmts)
		}

		return true
	})

	return subs, nil
}

type pipeCtx struct {
	inPipe bool
	start  bool
	end    bool
}

type collector struct {
	out *[]subcommand
}

func (c *collector) stmt(st *syntax.Stmt, ctx pipeCtx) {
	switch cmd := st.Cmd.(type) {
	case *syntax.CallExpr:
		c.emit(cmd, st.Redirs, ctx)
	case *syntax.BinaryCmd:
		if cmd.Op == syntax.Pipe || cmd.Op == syntax.PipeAll {
			members := flattenPipe(cmd)
			for i, m := range members {
				c.stmt(m, pipeCtx{inPipe: true, start: i == 0, end: i == len(members)-1})
			}
		} else { // && or || : independent statements
			c.stmt(cmd.X, pipeCtx{})
			c.stmt(cmd.Y, pipeCtx{})
		}
	case *syntax.Block:
		c.stmts(cmd.Stmts)
	case *syntax.Subshell:
		c.stmts(cmd.Stmts)
	case *syntax.IfClause:
		c.ifClause(cmd)
	case *syntax.WhileClause:
		c.stmts(cmd.Cond)
		c.stmts(cmd.Do)
	case *syntax.ForClause:
		c.stmts(cmd.Do)
	case *syntax.CaseClause:
		for _, item := range cmd.Items {
			c.stmts(item.Stmts)
		}
	default:
		// Any construct not handled explicitly: harvest nested simple commands
		// as standalone so nothing silently escapes evaluation.
		syntax.Walk(cmd, func(n syntax.Node) bool {
			if call, ok := n.(*syntax.CallExpr); ok {
				c.emit(call, nil, pipeCtx{})
				return false
			}

			return true
		})
	}
}

func (c *collector) stmts(stmts []*syntax.Stmt) {
	for _, st := range stmts {
		c.stmt(st, pipeCtx{})
	}
}

func (c *collector) ifClause(cmd *syntax.IfClause) {
	c.stmts(cmd.Cond)
	c.stmts(cmd.Then)

	if cmd.Else != nil {
		c.ifClause(cmd.Else)
	}
}

func (c *collector) emit(call *syntax.CallExpr, redirs []*syntax.Redirect, ctx pipeCtx) {
	sub := subcommand{
		inPipe:    ctx.inPipe,
		pipeStart: ctx.start,
		pipeEnd:   ctx.end,
	}

	for _, a := range call.Assigns {
		sub.tokens = append(sub.tokens, assignToken(a))
	}

	for _, w := range call.Args {
		sub.tokens = append(sub.tokens, wordToken(w))
	}

	for _, r := range redirs {
		sub.redirs = append(sub.redirs, redir{safe: safeRedirect(r)})
	}

	// A subcommand with no command name (e.g. a bare assignment) has nothing to
	// match; skip it.
	if len(sub.tokens) == 0 {
		return
	}

	*c.out = append(*c.out, sub)
}

// flattenPipe returns the pipeline members of a Pipe/PipeAll BinaryCmd in order.
func flattenPipe(b *syntax.BinaryCmd) []*syntax.Stmt {
	var members []*syntax.Stmt

	var add func(st *syntax.Stmt)

	add = func(st *syntax.Stmt) {
		if bb, ok := st.Cmd.(*syntax.BinaryCmd); ok && (bb.Op == syntax.Pipe || bb.Op == syntax.PipeAll) {
			add(bb.X)
			add(bb.Y)

			return
		}

		members = append(members, st)
	}

	add(b.X)
	add(b.Y)

	return members
}

func assignToken(a *syntax.Assign) token {
	var b strings.Builder

	if a.Name != nil {
		b.WriteString(a.Name.Value)
	}

	lit := true

	if !a.Naked {
		b.WriteByte('=')

		if a.Value != nil {
			t := wordToken(a.Value)
			b.WriteString(t.text)
			lit = t.literal
		}
	}

	return token{text: b.String(), kind: tokAssign, literal: lit}
}

// wordToken renders a word to its best-effort literal text and reports whether
// it is a pure literal (no shell expansions). A word that contains a parameter
// or command expansion is not literal and cannot match @arg/@path/@int, since
// it may expand to zero or many arguments.
func wordToken(w *syntax.Word) token {
	var (
		b       strings.Builder
		literal = true
	)

	var render func(parts []syntax.WordPart)

	render = func(parts []syntax.WordPart) {
		for _, p := range parts {
			switch part := p.(type) {
			case *syntax.Lit:
				b.WriteString(part.Value)
			case *syntax.SglQuoted:
				b.WriteString(part.Value)
			case *syntax.DblQuoted:
				render(part.Parts)
			default:
				// ParamExp, CmdSubst, ArithmExp, ProcSubst, ExtGlob, ...
				literal = false
			}
		}
	}

	render(w.Parts)

	return token{text: b.String(), kind: tokWord, literal: literal}
}

// safeRedirect reports whether a redirection is non-destructive: an input
// redirect, or an output redirect to /dev/null.
func safeRedirect(r *syntax.Redirect) bool {
	op := r.Op.String()
	if strings.Contains(op, "<") && !strings.Contains(op, ">") {
		return true
	}

	if r.Word != nil {
		if t := wordToken(r.Word); t.literal && t.text == "/dev/null" {
			return true
		}
	}

	return false
}
