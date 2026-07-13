package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// WhyQuery describes a single allow/deny question to ask nono's oracle
// (`nono why`). Exactly one of the two query shapes is used:
//
//   - a filesystem query: Path set, Op one of "read"/"write"/"readwrite".
//   - a network query: Host set, Port optional (defaults to 443).
//
// The query is evaluated against the graith-generated profile in WrapOpts, so
// the answer reflects graith's own sandbox policy, not nono's bare defaults.
type WhyQuery struct {
	// Path + Op form a filesystem query.
	Path string
	Op   string // "read", "write", or "readwrite"

	// Host + Port form a network query.
	Host string
	Port int
}

// validOps are the operations nono's `--op` accepts.
var validOps = map[string]struct{}{
	"read":      {},
	"write":     {},
	"readwrite": {},
}

// Validate reports whether the query is well-formed: exactly one of the
// filesystem (Path/Op) or network (Host) shapes, with a valid Op.
func (q WhyQuery) Validate() error {
	fsQuery := q.Path != ""
	netQuery := q.Host != ""

	switch {
	case fsQuery && netQuery:
		return fmt.Errorf("provide either --path or --host, not both")
	case !fsQuery && !netQuery:
		return fmt.Errorf("provide --path <p> --op <read|write|readwrite> or --host <h>")
	case fsQuery:
		if q.Op == "" {
			return fmt.Errorf("--op is required with --path (read, write, or readwrite)")
		}

		if _, ok := validOps[q.Op]; !ok {
			return fmt.Errorf("invalid --op %q (want read, write, or readwrite)", q.Op)
		}
	}

	return nil
}

// WhyResult is the decoded answer from `nono why --json`. nono's schema varies
// by query kind; the common fields are Status ("allowed"/"denied") and Reason.
// The remaining fields are populated best-effort from whichever keys nono emits
// so graith can render a useful explanation without pinning every variant.
type WhyResult struct {
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	Details      string `json:"details,omitempty"`
	Access       string `json:"access,omitempty"`
	GrantedPath  string `json:"granted_path,omitempty"`
	Source       string `json:"source,omitempty"`
	PolicySource string `json:"policy_source,omitempty"`
	SuggestFlag  string `json:"suggested_flag,omitempty"`
}

// Allowed reports whether the queried access would be permitted.
func (r WhyResult) Allowed() bool { return r.Status == "allowed" }

// Explanation renders a one-line human summary of the decision, folding in
// whichever explanatory fields nono provided.
func (r WhyResult) Explanation() string {
	var b strings.Builder

	if r.Allowed() {
		b.WriteString("allowed")
	} else {
		b.WriteString("denied")
	}

	// Prefer the most specific explanation nono offers.
	switch {
	case r.Details != "":
		b.WriteString(": " + r.Details)
	case r.Access != "":
		b.WriteString(": " + r.Access)
	case r.Reason != "":
		b.WriteString(" (" + r.Reason + ")")
	}

	if src := r.sourceLabel(); src != "" {
		b.WriteString(" [" + src + "]")
	}

	if r.SuggestFlag != "" {
		b.WriteString(" — suggested: " + r.SuggestFlag)
	}

	return b.String()
}

func (r WhyResult) sourceLabel() string {
	if r.Source != "" {
		return r.Source
	}

	return r.PolicySource
}

// nonoWhyArgs builds the `nono why` argv for a query evaluated against the
// profile at profilePath. It always requests JSON and passes the profile as the
// query context so the answer reflects graith's policy.
func nonoWhyArgs(profilePath string, q WhyQuery) []string {
	args := []string{"why", "--json", "-p", profilePath}

	if q.Path != "" {
		args = append(args, "--path", q.Path, "--op", q.Op)
	}

	if q.Host != "" {
		args = append(args, "--host", q.Host)

		port := q.Port
		if port == 0 {
			port = 443
		}

		args = append(args, "--port", strconv.Itoa(port))
	}

	return args
}

// nonoRunner runs a nono subcommand and returns its stdout, stderr, and error.
// It is a seam so WhyForProfile is unit-testable without nono installed.
type nonoRunner func(command string, args []string) (stdout, stderr string, err error)

func execNono(command string, args []string) (string, string, error) {
	if command == "" {
		command = BackendNono
	}

	cmd := exec.Command(command, args...)

	var out, errOut strings.Builder

	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()

	return out.String(), errOut.String(), err
}

// WhyForProfile answers a WhyQuery by shelling out to `nono why` against a
// graith-generated profile. It is the introspection primitive behind
// `gr sandbox explain`. command overrides the nono binary ("" uses the default).
//
// nono emits its JSON decision on stdout and exits 0 for both allow and deny;
// a decode failure (or non-empty stderr with no JSON) is reported as an error
// so a nono CLI shift surfaces loudly rather than silently misreporting.
func WhyForProfile(command, profilePath string, q WhyQuery) (WhyResult, error) {
	return whyForProfile(command, profilePath, q, execNono)
}

func whyForProfile(command, profilePath string, q WhyQuery, run nonoRunner) (WhyResult, error) {
	if err := q.Validate(); err != nil {
		return WhyResult{}, err
	}

	stdout, stderr, err := run(command, nonoWhyArgs(profilePath, q))

	var res WhyResult
	if decodeErr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); decodeErr != nil {
		// No parseable decision. Surface nono's stderr (arg errors, missing
		// binary) rather than pretend we got an answer.
		detail := strings.TrimSpace(stderr)
		if detail == "" && err != nil {
			detail = err.Error()
		}

		if detail == "" {
			detail = "no JSON decision on stdout"
		}

		return WhyResult{}, fmt.Errorf("nono why failed: %s", detail)
	}

	if res.Status == "" {
		return WhyResult{}, fmt.Errorf("nono why returned no decision status")
	}

	return res, nil
}

// BuildQueryProfile compiles a graith policy (opts) into a nono profile and
// writes it to a temp file for use as `nono why` query context. It reuses the
// same profile emitter as the run path, so `gr sandbox explain` answers reflect the
// exact profile a real session would run under. The caller must remove the
// returned path; warnings mirror those the run path would emit.
func BuildQueryProfile(opts WrapOpts) (path string, warnings []string, err error) {
	name := opts.profileName()

	profile, warnings, err := buildNonoProfile(name, opts, os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		return "", warnings, err
	}

	path, err = writeNonoProfile(profile, "")
	if err != nil {
		return "", warnings, fmt.Errorf("write query profile: %w", err)
	}

	return path, warnings, nil
}
