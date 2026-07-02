package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/spf13/cobra"
)

var sandboxCmd = &cobra.Command{
	Use:   "sandbox",
	Short: "Inspect the sandbox policy",
	Long: `Inspect the sandbox policy graith would apply.

Subcommands help you understand what the configured sandbox would allow or
deny for a given path or network host, so you can debug a confusing denial
without launching an agent.`,
}

var (
	whyPath  string
	whyOp    string
	whyHost  string
	whyPort  int
	whyAgent string
)

var sandboxWhyCmd = &cobra.Command{
	Use:   "why",
	Short: "Explain whether the sandbox would allow or deny an access",
	Long: `Explain an allow/deny decision under graith's configured sandbox.

Builds the nono profile graith would generate from your config (global merged
with an optional per-agent override) and asks nono's oracle whether a given
filesystem or network access would be permitted, then explains the decision.

Examples:
  gr sandbox why --path ~/.ssh/id_rsa --op read
  gr sandbox why --path ./src --op write
  gr sandbox why --host github.com --port 443
  gr sandbox why --agent codex --path /etc/hosts --op read

This command targets the "nono" backend (the only backend with a policy
oracle). It does not launch an agent and makes no changes.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runSandboxWhy()
	},
}

// whyOutput is the JSON shape emitted with --json.
type whyOutput struct {
	Backend     string   `json:"backend"`
	Query       whyQuery `json:"query"`
	Allowed     bool     `json:"allowed"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason,omitempty"`
	Details     string   `json:"details,omitempty"`
	Source      string   `json:"source,omitempty"`
	Suggested   string   `json:"suggested_flag,omitempty"`
	Explanation string   `json:"explanation"`
	Warnings    []string `json:"warnings,omitempty"`
}

type whyQuery struct {
	Path string `json:"path,omitempty"`
	Op   string `json:"op,omitempty"`
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
}

func runSandboxWhy() error {
	query := sandbox.WhyQuery{Path: whyPath, Op: whyOp, Host: whyHost, Port: whyPort}
	if err := query.Validate(); err != nil {
		return err
	}

	merged := cfg.Sandbox
	if whyAgent != "" {
		merged = cfg.Sandbox.Merge(cfg.Agents[whyAgent].Sandbox)
	}

	if merged.Backend != sandbox.BackendNono {
		backend := merged.Backend
		if backend == "" {
			backend = "(unset)"
		}

		return fmt.Errorf(
			"gr sandbox why requires the %q backend (policy oracle); configured backend is %q",
			sandbox.BackendNono, backend)
	}

	// Confirm nono can be reached before building a profile, so the error is
	// about the backend, not a stray decision.
	avail, err := sandbox.CheckAvailability(merged.Backend, merged.Command)
	if err != nil {
		return err
	}

	if !avail.CanEnforce {
		return fmt.Errorf("nono backend cannot enforce here: %s", avail.Detail)
	}

	opts := whyWrapOpts(merged)

	profilePath, warnings, err := sandbox.BuildQueryProfile(opts)
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(profilePath) }()

	res, err := sandbox.WhyForProfile(merged.Command, profilePath, query)
	if err != nil {
		return err
	}

	return renderWhy(merged.Backend, query, res, warnings)
}

// whyWrapOpts builds the policy-relevant WrapOpts for a query. It mirrors the
// daemon's expansion (ExpandPath + globbing) but omits session-specific grants
// (hook dir, runtime dir, agent binary dir) that the daemon adds at launch —
// those are not part of the user-configured policy the user is reasoning about.
// PATH/HOME are added because the daemon always includes them and a query
// against the env would otherwise misreport.
func whyWrapOpts(merged config.SandboxConfig) sandbox.WrapOpts {
	envKeys := ensureWhyEnvKeys(nil, "PATH", "HOME")

	return sandbox.WrapOpts{
		Backend:        merged.Backend,
		ReadDirs:       expandWhyPaths(merged.ReadDirs),
		WriteDirs:      expandWhyPaths(merged.WriteDirs),
		Features:       merged.Features,
		EnvKeys:        envKeys,
		BackendCommand: merged.Command,
	}
}

// expandWhyPaths mirrors the daemon's expandPaths: ~-expand, make absolute,
// glob-expand, and drop entries that do not exist (matching the lenient run
// behaviour). It has no logger, so silent skips are fine for a read-only query.
func expandWhyPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	var out []string

	for _, p := range paths {
		expanded := config.ExpandPath(p)

		if strings.ContainsAny(expanded, "*?[") {
			if matches, err := filepath.Glob(expanded); err == nil {
				for _, m := range matches {
					if _, statErr := os.Stat(m); statErr == nil {
						out = append(out, m)
					}
				}
			}

			continue
		}

		if _, err := os.Stat(expanded); err == nil {
			out = append(out, expanded)
		}
	}

	return out
}

func ensureWhyEnvKeys(keys []string, want ...string) []string {
	have := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		have[k] = struct{}{}
	}

	for _, w := range want {
		if _, ok := have[w]; !ok {
			keys = append(keys, w)
			have[w] = struct{}{}
		}
	}

	return keys
}

func renderWhy(backend string, q sandbox.WhyQuery, res sandbox.WhyResult, warnings []string) error {
	if jsonOutput {
		return out.JSON(whyOutput{
			Backend:     backend,
			Query:       whyQuery{Path: q.Path, Op: q.Op, Host: q.Host, Port: q.Port},
			Allowed:     res.Allowed(),
			Status:      res.Status,
			Reason:      res.Reason,
			Details:     res.Details,
			Source:      whySource(res),
			Suggested:   res.SuggestFlag,
			Explanation: res.Explanation(),
			Warnings:    warnings,
		})
	}

	for _, w := range warnings {
		out.Printf("warning: %s\n", w)
	}

	subject := q.Path
	if subject == "" {
		subject = q.Host
	}

	verb := q.Op
	if verb == "" {
		verb = "connect"
	}

	out.Printf("%s %s: %s\n", verb, subject, res.Explanation())

	return nil
}

func whySource(res sandbox.WhyResult) string {
	if res.Source != "" {
		return res.Source
	}

	return res.PolicySource
}

// registerSandboxCmd registers this command on rootCmd. Called from registerCommands.
func registerSandboxCmd() {
	sandboxWhyCmd.Flags().StringVar(&whyPath, "path", "", "filesystem path to check")
	sandboxWhyCmd.Flags().StringVar(&whyOp, "op", "", "operation for --path: read, write, or readwrite")
	sandboxWhyCmd.Flags().StringVar(&whyHost, "host", "", "network host to check (e.g. github.com)")
	sandboxWhyCmd.Flags().IntVar(&whyPort, "port", 0, "network port for --host (default 443)")
	sandboxWhyCmd.Flags().StringVar(&whyAgent, "agent", "", "resolve the policy for this agent's merged sandbox config")

	sandboxCmd.AddCommand(sandboxWhyCmd)
	rootCmd.AddCommand(sandboxCmd)
}
