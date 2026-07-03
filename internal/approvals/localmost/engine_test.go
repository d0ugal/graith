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
}
