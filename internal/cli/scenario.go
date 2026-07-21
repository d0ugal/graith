package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/scenariofile"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type scenarioFile = scenariofile.File
type scenarioFileMeta = scenariofile.Meta
type scenarioFileSession = scenariofile.Session

const (
	scenarioListTableHeader = "  NAME\tID\tSTATUS\tSESSIONS\tGOAL\tPOLICY PROGRESS\n"
)

func scenariosDir() string {
	return filepath.Join(filepath.Dir(paths.ConfigFile), "scenarios")
}

func resolveScenarioSource(source string) ([]byte, error) {
	return resolveScenarioSourceFrom(source, scenariosDir())
}

func resolveScenarioSourceFrom(source, dir string) ([]byte, error) {
	if source == "-" {
		return io.ReadAll(os.Stdin)
	}

	if strings.HasPrefix(source, "store:") {
		return nil, errors.New("store: prefix not yet implemented — use a file path or stdin (-)")
	}

	// Try as literal path first.
	if _, err := os.Stat(source); err == nil {
		return os.ReadFile(source)
	}

	// Try name-based lookup in the scenarios directory.
	name := source
	if !strings.HasSuffix(name, ".toml") {
		name += ".toml"
	}

	candidate := filepath.Join(dir, name)
	if data, err := os.ReadFile(candidate); err == nil {
		return data, nil
	}

	return nil, fmt.Errorf("scenario file not found: %s (also checked %s)", source, candidate)
}

func parseScenarioFile(data []byte) (*scenarioFile, error) {
	sf, err := scenariofile.Parse(data)
	if err != nil {
		return nil, err
	}

	// Available scenario discovery parses files without building session inputs,
	// so retain its ordinary-member repo check while allowing derived repos.
	for _, session := range sf.Sessions {
		if session.Repo == "" && !session.Shared && session.Mirror == "" {
			return nil, fmt.Errorf("session %q: repo is required", session.Name)
		}
	}

	return sf, nil
}

func scenarioPolicyInput(policy *scenariofile.PolicyConfig) *protocol.ScenarioPolicyInput {
	return scenariofile.PolicyInput(policy)
}

func scenarioMemberPolicyInput(policy *scenariofile.MemberPolicyConfig) *protocol.ScenarioMemberPolicyInput {
	return scenariofile.MemberPolicyInput(policy)
}

func buildSessionInputs(sf *scenarioFile) ([]protocol.ScenarioSessionInput, error) {
	var (
		sessions      = make([]protocol.ScenarioSessionInput, len(sf.Sessions))
		mirrorMembers = make([]scenariofile.MirrorMember, len(sf.Sessions))
	)

	for i, s := range sf.Sessions {
		if s.Name == "" {
			return nil, fmt.Errorf("session %d: name is required", i)
		}

		if s.Repo == "" && !s.Shared && s.Mirror == "" {
			return nil, fmt.Errorf("session %q: repo is required", s.Name)
		}

		repo := ""
		if s.Repo != "" {
			repo = config.ExpandPath(s.Repo)
		}

		var includes []string
		if len(s.Includes) > 0 {
			includes = make([]string, len(s.Includes))
			for j, inc := range s.Includes {
				includes[j] = config.ExpandPath(inc)
			}
		}

		results := make([]protocol.ScenarioResultSpec, len(s.Results))
		for j, result := range s.Results {
			results[j] = protocol.ScenarioResultSpec{
				Name: result.Name, Format: result.Format, Store: result.Store, Required: result.Required,
			}
		}

		sessions[i] = protocol.ScenarioSessionInput{
			Name:       s.Name,
			Repo:       repo,
			Mirror:     s.Mirror,
			Agent:      s.Agent,
			Model:      s.Model,
			Base:       s.Base,
			Role:       s.Role,
			Prompt:     s.Prompt,
			Task:       s.Task,
			DependsOn:  append([]string(nil), s.DependsOn...),
			AgentHooks: s.AgentHooks == nil || *s.AgentHooks,
			Shared:     s.Shared,
			Includes:   includes,
			Star:       s.Star,
			Results:    results,
			Policy:     scenarioMemberPolicyInput(s.Policy),
		}
		mirrorMembers[i] = scenariofile.MirrorMember{
			Name: s.Name, Mirror: s.Mirror, Repo: s.Repo, Base: s.Base,
			Shared: s.Shared, Includes: len(s.Includes),
		}
	}

	templatedMemberGraph := scenariofile.HasTemplatedMemberGraph(sessions)
	if !templatedMemberGraph {
		if _, err := scenariofile.ValidateMirrorMembers(mirrorMembers); err != nil {
			return nil, err
		}
	}

	if err := scenariofile.ValidateSessionContracts(sessions, config.TodoMaxTitleCeiling); err != nil {
		return nil, err
	}

	scenariofile.NormalizeSessionContracts(sessions)

	if !templatedMemberGraph {
		if err := scenariofile.ValidateSessionDependencies(sessions); err != nil {
			return nil, err
		}
	}

	return sessions, nil
}

type availableScenario struct {
	Name string `json:"name"`
	Goal string `json:"goal"`
	File string `json:"file"`
}

type scenarioResultControlClient interface {
	SendControl(msgType string, payload any) error
	ReadControlResponse() (protocol.Envelope, error)
	Close()
}

var scenarioResultConnect = func() (scenarioResultControlClient, error) {
	return client.Connect(cfg, paths, cfgFile)
}

type scenarioControlSender interface {
	SendControl(msgType string, payload any) error
}

func sendScenarioControl(sender scenarioControlSender, msgType string, payload any) error {
	if err := sender.SendControl(msgType, payload); err != nil {
		return fmt.Errorf("send %s: %w", msgType, err)
	}

	return nil
}

func listAvailableScenarios() []availableScenario {
	return listAvailableScenariosIn(scenariosDir())
}

func listAvailableScenariosIn(dir string) []availableScenario {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var result []availableScenario

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}

		sf, err := parseScenarioFile(data)
		if err != nil {
			continue
		}

		name := strings.TrimSuffix(e.Name(), ".toml")
		result = append(result, availableScenario{
			Name: name,
			Goal: sf.Scenario.Goal,
			File: e.Name(),
		})
	}

	return result
}

var scenarioCmd = &cobra.Command{
	Use:   "scenario",
	Short: "Manage multi-session scenarios",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var scenarioStartCmd = &cobra.Command{
	Use:   "start <file-or-name>",
	Short: "Start a scenario from a TOML file, name, or stdin (-)",
	Long: `Start a multi-session scenario. Only the orchestrator session can start scenarios.

The source can be:
  - A file path (./scenario.toml or /path/to/scenario.toml)
  - A name that resolves to ~/.config/graith/scenarios/<name>.toml
  - "-" to read from stdin`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := resolveScenarioSource(args[0])
		if err != nil {
			return err
		}

		sf, err := parseScenarioFile(data)
		if err != nil {
			return err
		}

		sessions, err := buildSessionInputs(sf)
		if err != nil {
			return err
		}

		callerID := os.Getenv("GRAITH_SESSION_ID")
		if callerID == "" {
			return errors.New("GRAITH_SESSION_ID is not set — scenarios must be started from within a graith session")
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		if err := sendScenarioControl(c, "scenario_start", protocol.ScenarioStartMsg{
			CallerSessionID: callerID,
			Name:            sf.Scenario.Name,
			Goal:            sf.Scenario.Goal,
			Sessions:        sessions,
			Policy:          scenarioPolicyInput(sf.Scenario.Policy),
			Triggers:        sf.Triggers,
			Lifecycle:       sf.Scenario.Lifecycle,
		}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		var record protocol.ScenarioRecord

		_ = protocol.DecodePayload(resp, &record)

		if jsonOutput {
			return out.JSON(record)
		}

		out.Printf("Scenario %q started (id: %s)\n", record.Name, record.ID)

		for _, s := range record.Sessions {
			out.Printf("  %s (%s) — %s\n", s.Name, s.SessionID, s.Role)
		}

		return nil
	},
}

// runScenarioLifecycle sends a scenario lifecycle control message (stop,
// resume, delete) and returns the list of affected session names decoded from
// the response's resultKey field. When --json is set it prints the raw payload
// and returns handled=true so the caller skips its own formatting.
func runScenarioLifecycle(controlType string, payload any, resultKey string) (names []string, handled bool, err error) {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return nil, false, err
	}
	defer c.Close()

	if err := sendScenarioControl(c, controlType, payload); err != nil {
		return nil, false, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, false, err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return nil, false, fmt.Errorf("%s", e.Message)
	}

	if jsonOutput {
		return nil, true, out.JSON(resp.Payload)
	}

	names, err = decodeLifecycleResult(resp.Payload, resultKey)
	if err != nil {
		return nil, false, fmt.Errorf("%s response: %w", controlType, err)
	}

	return names, false, nil
}

// decodeLifecycleResult extracts the []string list stored under resultKey from a
// scenario lifecycle response payload. The daemon's success payloads mix value
// types (a string "name" field alongside the []string result), so we decode
// each field into a json.RawMessage first and only unmarshal resultKey. Decode
// errors are surfaced rather than swallowed so protocol drift is visible instead
// of silently reporting 0 sessions.
//
// A valid daemon success payload always carries resultKey, so an empty/null
// top-level payload or a missing key is treated as protocol drift and returns an
// error. A present-but-null result value (an empty result marshals to JSON null
// because the daemon builds it with `var stopped []string`) is a legitimate
// no-op and yields a nil slice with no error.
func decodeLifecycleResult(payload json.RawMessage, resultKey string) ([]string, error) {
	if len(payload) == 0 || string(payload) == "null" {
		return nil, errors.New("empty response payload")
	}

	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}

	raw, ok := fields[resultKey]
	if !ok {
		return nil, fmt.Errorf("response missing %q field", resultKey)
	}

	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, fmt.Errorf("decode %q field: %w", resultKey, err)
	}

	return names, nil
}

var scenarioStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop all sessions in a scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		stopped, handled, err := runScenarioLifecycle("scenario_stop", protocol.ScenarioStopMsg{Name: args[0]}, "stopped")
		if err != nil || handled {
			return err
		}

		out.Printf("Stopped %d sessions in scenario %q\n", len(stopped), args[0])

		return nil
	},
}

var scenarioResumeCmd = &cobra.Command{
	Use:   "resume <name>",
	Short: "Resume all stopped/errored sessions in a scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		resumed, handled, err := runScenarioLifecycle("scenario_resume", protocol.ScenarioResumeMsg{Name: args[0]}, "resumed")
		if err != nil || handled {
			return err
		}

		out.Printf("Resumed %d sessions in scenario %q\n", len(resumed), args[0])

		return nil
	},
}

var scenarioDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a scenario and all its sessions",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deleted, handled, err := runScenarioLifecycle("scenario_delete", protocol.ScenarioDeleteMsg{Name: args[0]}, "deleted")
		if err != nil || handled {
			return err
		}

		out.Printf("Deleted scenario %q (%d sessions removed)\n", args[0], len(deleted))

		return nil
	},
}

var scenarioStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show status of a scenario's sessions",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		if err := sendScenarioControl(c, "scenario_status", protocol.ScenarioStatusMsg{Name: args[0]}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		var statusResp protocol.ScenarioStatusResponse

		_ = protocol.DecodePayload(resp, &statusResp)

		if jsonOutput {
			return out.JSON(statusResp)
		}

		statusOut := cmd.OutOrStdout()
		renderScenarioStatus(statusOut, statusResp.Scenario, scenarioStatusWidth(statusOut))

		return nil
	},
}

const scenarioStatusLabelWidth = 14

// scenarioStatusWidth uses the real terminal width when stdout is a TTY. Pipes,
// redirected output, and terminal-probe failures use the configured lifecycle
// fallback geometry (80 columns by default), so human output never assumes that
// a terminal is present.
func scenarioStatusWidth(w io.Writer) int {
	fallbackCols, _ := client.FallbackGeometry()
	fallback := int(fallbackCols)
	if fallback < 1 {
		fallback = config.DefaultColsDefault
	}

	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return fallback
	}

	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width < 1 {
		return fallback
	}

	return width
}

// renderScenarioStatus deliberately uses vertical member blocks instead of a
// wide table. Every physical line is bounded by width, and identifiers use a
// display-width-aware middle ellipsis so members with a shared templated prefix
// remain distinguishable by their suffix. Result entries get one line each so
// a terminal can never split a name=status pair away from its member.
func renderScenarioStatus(w io.Writer, sc protocol.ScenarioRecord, width int) {
	writeScenarioStatusHeading(w, width, "SCENARIO")
	writeScenarioStatusField(w, width, "NAME", []string{sc.Name}, true)
	writeScenarioStatusField(w, width, "ID", []string{sc.ID}, true)
	writeScenarioStatusField(w, width, "STATUS", []string{sc.Status}, false)
	writeScenarioStatusField(w, width, "GOAL", []string{sc.Goal}, false)

	if sc.Render != nil {
		writeScenarioStatusField(w, width, "AUTHORED NAME", []string{sc.Render.AuthoredName}, true)
		writeScenarioStatusField(w, width, "RENDER CONTEXT", []string{fmt.Sprintf("caller=%s parent=%s initiator=%s at %s",
			sc.Render.Caller.Name, sc.Render.Parent.Name, sc.Render.Initiator.Name, sc.Render.RenderedAt)}, false)
	}

	if sc.Policy != nil {
		writeScenarioStatusField(w, width, "POLICY", []string{formatScenarioPolicySummary(sc.Policy)}, false)
		if sc.Policy.OutcomeReason != "" {
			writeScenarioStatusField(w, width, "OUTCOME", []string{sc.Policy.OutcomeReason}, false)
		}
	}

	_, _ = fmt.Fprintln(w)
	writeScenarioStatusHeading(w, width, fmt.Sprintf("MEMBERS (%d)", len(sc.Sessions)))

	for i, s := range sc.Sessions {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}

		writeScenarioStatusHeading(w, width, fmt.Sprintf("MEMBER %d/%d", i+1, len(sc.Sessions)))
		renderScenarioStatusMember(w, width, s)
	}

	if sc.CompletionEpoch > 0 {
		_, _ = fmt.Fprintln(w)
		writeScenarioStatusHeading(w, width, "COMPLETION")
		writeScenarioStatusField(w, width, "EPOCH", []string{fmt.Sprintf("%d", sc.CompletionEpoch)}, false)

		for _, action := range sc.CompletionActions {
			detail := action.Result
			if action.Error != "" {
				detail = action.Error
			}

			value := action.Name + ": " + action.State
			if detail != "" {
				value += " — " + detail
			}

			writeScenarioStatusField(w, width, "ACTION", []string{value}, false)
		}

		if sc.Cleanup != nil {
			value := fmt.Sprintf("%s (%s)", sc.Cleanup.State, sc.Cleanup.Policy)
			if sc.Cleanup.ScheduledAt != "" {
				value += " at " + sc.Cleanup.ScheduledAt
			}
			if sc.Cleanup.Error != "" {
				value += " — " + sc.Cleanup.Error
			}

			writeScenarioStatusField(w, width, "CLEANUP", []string{value}, false)
		}
	}
}

func renderScenarioStatusMember(w io.Writer, width int, s protocol.ScenarioSessionInfo) {
	// Progress is derived from the member's assigned todo items (issue #591):
	// done/total, or "—" when the member has no tracked work.
	progress := "—"
	if s.TodoTotal > 0 {
		progress = fmt.Sprintf("%d/%d", s.TodoDone, s.TodoTotal)
	}

	shared := "no"
	if s.Shared {
		shared = "yes"
	}

	required, attempt, deadline, policyResult := formatScenarioMemberPolicy(s.Policy)

	writeScenarioStatusField(w, width, "NAME", []string{s.Name}, true)
	writeScenarioStatusField(w, width, "SESSION", []string{s.SessionID}, true)
	writeScenarioStatusField(w, width, "STATUS", []string{s.Status}, false)
	writeScenarioStatusField(w, width, "AGENT", []string{s.Agent}, false)
	writeScenarioStatusField(w, width, "ROLE", []string{s.Role}, false)
	writeScenarioStatusField(w, width, "PROGRESS", []string{progress}, false)
	writeScenarioStatusField(w, width, "WAITING ON", s.BlockedBy, true)
	writeScenarioStatusField(w, width, "MIRROR", []string{s.Mirror}, true)
	writeScenarioStatusField(w, width, "RESULTS", formatScenarioResultStatuses(s.Results), true)
	writeScenarioStatusField(w, width, "SHARED", []string{shared}, false)
	writeScenarioStatusField(w, width, "REQUIRED", []string{required}, false)
	writeScenarioStatusField(w, width, "ATTEMPT", []string{attempt}, false)
	writeScenarioStatusField(w, width, "DEADLINE", []string{deadline}, false)
	writeScenarioStatusField(w, width, "POLICY RESULT", []string{policyResult}, false)
}

func writeScenarioStatusHeading(w io.Writer, width int, heading string) {
	_, _ = fmt.Fprintln(w, truncateScenarioStatusValue(heading, width, false))
}

func writeScenarioStatusField(w io.Writer, width int, label string, values []string, middle bool) {
	if len(values) == 0 {
		values = []string{"—"}
	}

	for i, value := range values {
		value = emptyScenarioStatusValue(value)

		fieldLabel := label
		if i > 0 {
			fieldLabel = ""
		}

		prefix := fmt.Sprintf("  %-*s  ", scenarioStatusLabelWidth, fieldLabel)
		valueWidth := width - ansi.StringWidth(prefix)
		if valueWidth < 1 {
			_, _ = fmt.Fprintln(w, truncateScenarioStatusValue(strings.TrimSpace(prefix)+" "+value, width, false))

			continue
		}

		_, _ = fmt.Fprintf(w, "%s%s\n", prefix, truncateScenarioStatusValue(value, valueWidth, middle))
	}
}

func emptyScenarioStatusValue(value string) string {
	value = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(value)
	if value == "" {
		return "—"
	}

	return value
}

func truncateScenarioStatusValue(value string, width int, middle bool) string {
	value = emptyScenarioStatusValue(value)
	if width < 1 {
		return ""
	}
	if ansi.StringWidth(value) <= width {
		return value
	}
	if !middle {
		return ansi.Truncate(value, width, "…")
	}
	if width == 1 {
		return "…"
	}

	contentWidth := width - 1
	leftWidth := (contentWidth + 1) / 2
	rightWidth := contentWidth - leftWidth
	totalWidth := ansi.StringWidth(value)

	return ansi.Cut(value, 0, leftWidth) + "…" + ansi.Cut(value, totalWidth-rightWidth, totalWidth)
}

func formatScenarioResultStatuses(results []protocol.ScenarioResultInfo) []string {
	if len(results) == 0 {
		return nil
	}

	parts := make([]string, len(results))
	for i, result := range results {
		parts[i] = result.Name + "=" + result.Status
	}

	return parts
}

func formatScenarioResultStatus(results []protocol.ScenarioResultInfo) string {
	parts := formatScenarioResultStatuses(results)
	if len(parts) == 0 {
		return "—"
	}

	return strings.Join(parts, ",")
}

var (
	scenarioResultFile     string
	scenarioResultScenario string
)

var scenarioResultCmd = &cobra.Command{
	Use:   "result",
	Short: "Publish declared scenario results",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var scenarioResultPutCmd = &cobra.Command{
	Use:   "put <name> [body]",
	Short: "Publish this member's declared result",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(_ *cobra.Command, args []string) error {
		body, err := resolveBody(args[1:], scenarioResultFile)
		if err != nil {
			return err
		}

		if len(body) > protocol.MaxScenarioResultBodyBytes {
			return fmt.Errorf("result is too large: %d bytes (max %d)", len(body), protocol.MaxScenarioResultBodyBytes)
		}

		scenarioName := scenarioResultScenario
		if scenarioName == "" {
			scenarioName = os.Getenv("GRAITH_SCENARIO_NAME")
		}

		c, err := scenarioResultConnect()
		if err != nil {
			return err
		}
		defer c.Close()

		if err := c.SendControl("scenario_result_publish", protocol.ScenarioResultPublishMsg{
			Scenario: scenarioName,
			Name:     args[0],
			Body:     body,
		}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var responseErr protocol.ErrorMsg
			if err := protocol.DecodePayload(resp, &responseErr); err != nil {
				return fmt.Errorf("decode scenario result error: %w", err)
			}

			return errors.New(responseErr.Message)
		}

		if resp.Type != "scenario_result_published" {
			return fmt.Errorf("unexpected scenario result response: %s", resp.Type)
		}

		var published protocol.ScenarioResultPublishResponse
		if err := protocol.DecodePayload(resp, &published); err != nil {
			return fmt.Errorf("decode scenario result response: %w", err)
		}

		if jsonOutput {
			return out.JSON(published)
		}

		out.Printf("Published %s result %q for %s in scenario %q (%d bytes)\n",
			published.Result.Format, published.Result.Name, published.Member,
			published.Scenario, published.Result.SizeBytes)
		out.Printf("Store: %s\n", published.Result.Destination)

		return nil
	},
}

var scenarioAddCmd = &cobra.Command{
	Use:   "add <scenario-name>",
	Short: "Add a new session to a running scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		repo, _ := cmd.Flags().GetString("repo")
		agent, _ := cmd.Flags().GetString("agent")
		model, _ := cmd.Flags().GetString("model")
		role, _ := cmd.Flags().GetString("role")
		prompt, _ := cmd.Flags().GetString("prompt")
		task, _ := cmd.Flags().GetString("task")
		dependsOn, _ := cmd.Flags().GetStringArray("depends-on")
		base, _ := cmd.Flags().GetString("base")
		optional, _ := cmd.Flags().GetBool("optional")
		timeout, _ := cmd.Flags().GetString("timeout")
		retries, _ := cmd.Flags().GetInt("retries")

		if name == "" {
			return errors.New("--name is required")
		}

		if repo == "" {
			return errors.New("--repo is required")
		}

		repo = config.ExpandPath(repo)

		var memberPolicy *protocol.ScenarioMemberPolicyInput
		if optional || cmd.Flags().Changed("timeout") || cmd.Flags().Changed("retries") {
			memberPolicy = &protocol.ScenarioMemberPolicyInput{Timeout: timeout, Retries: retries}

			if optional {
				required := false
				memberPolicy.Required = &required
			}
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		if err := sendScenarioControl(c, "scenario_add", protocol.ScenarioAddMsg{
			Name: args[0],
			Session: protocol.ScenarioSessionInput{
				Name:       name,
				Repo:       repo,
				Agent:      agent,
				Model:      model,
				Base:       base,
				Role:       role,
				Prompt:     prompt,
				Task:       task,
				DependsOn:  dependsOn,
				AgentHooks: true,
				Policy:     memberPolicy,
			},
		}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		if jsonOutput {
			return out.JSON(resp.Payload)
		}

		var result struct {
			SessionID string `json:"session_id"`
		}

		_ = protocol.DecodePayload(resp, &result)
		out.Printf("Added session %q to scenario %q (id: %s)\n", name, args[0], result.SessionID)

		return nil
	},
}

var scenarioListCmd = &cobra.Command{
	Use:   "list",
	Short: "List running scenarios and available scenario files",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		if err := sendScenarioControl(c, "scenario_list", protocol.ScenarioListMsg{}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		var listResp protocol.ScenarioListResponse

		_ = protocol.DecodePayload(resp, &listResp)

		available := listAvailableScenarios()

		if jsonOutput {
			return out.JSON(struct {
				Scenarios []protocol.ScenarioRecord `json:"scenarios"`
				Available []availableScenario       `json:"available"`
			}{listResp.Scenarios, available})
		}

		if len(listResp.Scenarios) > 0 {
			out.Printf("RUNNING SCENARIOS\n")

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprint(tw, scenarioListTableHeader)

			for _, sc := range listResp.Scenarios {
				goal := sc.Goal
				if len(goal) > 60 {
					goal = goal[:57] + "..."
				}

				policyProgress := formatScenarioPolicyProgress(sc.Policy)

				_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\t%s\n", sc.Name, sc.ID, sc.Status, len(sc.Sessions), goal, policyProgress)
			}

			_ = tw.Flush()
		} else {
			out.Printf("No running scenarios\n")
		}

		if len(available) > 0 {
			out.Printf("\nAVAILABLE SCENARIOS (%s)\n", scenariosDir())

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)

			for _, a := range available {
				goal := a.Goal
				if len(goal) > 60 {
					goal = goal[:57] + "..."
				}

				_, _ = fmt.Fprintf(tw, "  %s\t— %s\n", a.File, goal)
			}

			_ = tw.Flush()
		}

		return nil
	},
}

func formatScenarioPolicySummary(policy *protocol.ScenarioPolicyInfo) string {
	if policy == nil {
		return ""
	}

	threshold := fmt.Sprintf("all required (%d)", policy.RequiredTotal)
	if policy.Completion == scenariofile.CompletionQuorum {
		threshold = fmt.Sprintf("quorum %d", policy.Quorum)
	}

	summary := fmt.Sprintf("%s; %d successful; required %d/%d; on exhausted %s",
		threshold, policy.Successful, policy.RequiredSuccessful, policy.RequiredTotal, policy.OnExhausted)
	if policy.Paused {
		summary += "; paused"
	}

	return summary
}

func formatScenarioPolicyProgress(policy *protocol.ScenarioPolicyInfo) string {
	if policy == nil {
		return "—"
	}

	if policy.Completion == scenariofile.CompletionQuorum {
		return fmt.Sprintf("%d/%d quorum; required %d/%d", policy.Successful, policy.Quorum, policy.RequiredSuccessful, policy.RequiredTotal)
	}

	return fmt.Sprintf("required %d/%d", policy.RequiredSuccessful, policy.RequiredTotal)
}

func formatScenarioMemberPolicy(policy *protocol.ScenarioMemberPolicyInfo) (required, attempt, deadline, result string) {
	if policy == nil {
		return "—", "—", "—", "—"
	}

	required = "no"
	if policy.Required {
		required = "yes"
	}

	attempt = fmt.Sprintf("%d/%d", policy.Attempt, policy.MaxAttempts)

	deadline = policy.Deadline
	if deadline == "" {
		deadline = "—"
	}

	switch {
	case policy.SucceededAt != "":
		result = "succeeded"
	case policy.ExhaustionReason != "":
		result = policy.ExhaustionReason
	case policy.RetryPending:
		result = "retry pending"
	default:
		result = "pending"
	}

	return required, attempt, deadline, result
}

// registerScenarioCmd registers this command on rootCmd. Called from registerCommands.
func registerScenarioCmd() {
	scenarioCmd.AddCommand(scenarioStartCmd)
	scenarioCmd.AddCommand(scenarioStopCmd)
	scenarioCmd.AddCommand(scenarioResumeCmd)
	scenarioCmd.AddCommand(scenarioDeleteCmd)
	scenarioCmd.AddCommand(scenarioStatusCmd)
	scenarioCmd.AddCommand(scenarioAddCmd)
	scenarioCmd.AddCommand(scenarioListCmd)
	scenarioCmd.AddCommand(scenarioResultCmd)
	scenarioResultCmd.AddCommand(scenarioResultPutCmd)

	scenarioAddCmd.Flags().String("name", "", "Session name (required)")
	scenarioAddCmd.Flags().String("repo", "", "Repository path (required)")
	scenarioAddCmd.Flags().String("agent", "", "Agent type")
	scenarioAddCmd.Flags().String("model", "", "Model override")
	scenarioAddCmd.Flags().String("role", "", "Session role")
	scenarioAddCmd.Flags().String("prompt", "", "Startup prompt (defaults to --task)")
	scenarioAddCmd.Flags().String("task", "", "Tracked task used to seed an assigned todo")
	scenarioAddCmd.Flags().StringArray("depends-on", nil, "Member whose seeded task must finish first (repeatable)")
	scenarioAddCmd.Flags().String("base", "", "Base branch")
	scenarioResultPutCmd.Flags().StringVar(&scenarioResultFile, "file", "", "Read result body from file")
	scenarioResultPutCmd.Flags().StringVar(&scenarioResultScenario, "scenario", "", "Scenario name (defaults to GRAITH_SCENARIO_NAME)")
	scenarioAddCmd.Flags().Bool("optional", false, "Do not require this member for scenario completion")
	scenarioAddCmd.Flags().String("timeout", "", "Immutable per-attempt timeout (for example 30m)")
	scenarioAddCmd.Flags().Int("retries", 0, "Automatic retries after timeout (0-10; requires --timeout)")

	rootCmd.AddCommand(scenarioCmd)
}
