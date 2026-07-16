package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/scenariofile"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

type scenarioFile struct {
	Version  int                    `toml:"version"`
	Scenario scenarioFileMeta       `toml:"scenario"`
	Sessions []scenarioFileSession  `toml:"sessions"`
	Triggers []config.TriggerConfig `toml:"trigger"`
}

type scenarioFileMeta struct {
	Name string `toml:"name"`
	Goal string `toml:"goal"`
}

type scenarioFileSession struct {
	Name       string   `toml:"name"`
	Repo       string   `toml:"repo"`
	Agent      string   `toml:"agent"`
	Model      string   `toml:"model"`
	Base       string   `toml:"base"`
	Role       string   `toml:"role"`
	Task       string   `toml:"task"`
	AgentHooks *bool    `toml:"agent_hooks"`
	Shared     bool     `toml:"shared"`
	Includes   []string `toml:"includes"`
	Star       bool     `toml:"star"`
}

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
	var sf scenarioFile

	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&sf); err != nil {
		return nil, fmt.Errorf("parse scenario TOML: %w", err)
	}

	if sf.Version != 1 {
		return nil, fmt.Errorf("unsupported scenario version %d (expected 1)", sf.Version)
	}

	if sf.Scenario.Name == "" {
		return nil, errors.New("scenario.name is required")
	}

	if len(sf.Sessions) == 0 {
		return nil, errors.New("at least one [[sessions]] entry is required")
	}

	roles := make(map[string]bool, len(sf.Sessions))
	members := make(map[string]bool, len(sf.Sessions))

	for _, s := range sf.Sessions {
		// Shared members can't be watch-bound (they keep their own scenario
		// identity), so their role is not selectable; they remain valid delivery
		// targets by name.
		if s.Role != "" && !s.Shared {
			roles[s.Role] = true
		}

		if s.Name != "" {
			members[s.Name] = true
		}
	}

	if err := scenariofile.ValidateScenarioTriggers(sf.Triggers, roles, members); err != nil {
		return nil, err
	}

	return &sf, nil
}

func buildSessionInputs(sf *scenarioFile) ([]protocol.ScenarioSessionInput, error) {
	sessions := make([]protocol.ScenarioSessionInput, len(sf.Sessions))
	for i, s := range sf.Sessions {
		if s.Name == "" {
			return nil, fmt.Errorf("session %d: name is required", i)
		}

		if s.Repo == "" {
			return nil, fmt.Errorf("session %q: repo is required", s.Name)
		}

		repo := config.ExpandPath(s.Repo)

		var includes []string
		if len(s.Includes) > 0 {
			includes = make([]string, len(s.Includes))
			for j, inc := range s.Includes {
				includes[j] = config.ExpandPath(inc)
			}
		}

		sessions[i] = protocol.ScenarioSessionInput{
			Name:       s.Name,
			Repo:       repo,
			Agent:      s.Agent,
			Model:      s.Model,
			Base:       s.Base,
			Role:       s.Role,
			Task:       s.Task,
			AgentHooks: s.AgentHooks == nil || *s.AgentHooks,
			Shared:     s.Shared,
			Includes:   includes,
			Star:       s.Star,
		}
	}

	return sessions, nil
}

type availableScenario struct {
	Name string `json:"name"`
	Goal string `json:"goal"`
	File string `json:"file"`
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

		_ = c.SendControl("scenario_start", protocol.ScenarioStartMsg{
			CallerSessionID: callerID,
			Name:            sf.Scenario.Name,
			Goal:            sf.Scenario.Goal,
			Sessions:        sessions,
			Triggers:        sf.Triggers,
		})

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

	_ = c.SendControl(controlType, payload)

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

		_ = c.SendControl("scenario_status", protocol.ScenarioStatusMsg{Name: args[0]})

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

		sc := statusResp.Scenario
		out.Printf("Scenario: %s (%s) — %s\n", sc.Name, sc.ID, sc.Status)
		out.Printf("Goal: %s\n\n", sc.Goal)

		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintf(tw, "NAME\tSESSION\tSTATUS\tAGENT\tROLE\tPROGRESS\tSHARED\n")

		for _, s := range sc.Sessions {
			// Progress is derived from the member's assigned todo items (issue
			// #591): done/total, or "—" when the member has no tracked work.
			progress := "—"
			if s.TodoTotal > 0 {
				progress = fmt.Sprintf("%d/%d", s.TodoDone, s.TodoTotal)
			}

			shared := ""
			if s.Shared {
				shared = "yes"
			}

			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.SessionID, s.Status, s.Agent, s.Role, progress, shared)
		}

		_ = tw.Flush()

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
		task, _ := cmd.Flags().GetString("task")
		base, _ := cmd.Flags().GetString("base")

		if name == "" {
			return errors.New("--name is required")
		}

		if repo == "" {
			return errors.New("--repo is required")
		}

		repo = config.ExpandPath(repo)

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("scenario_add", protocol.ScenarioAddMsg{
			Name: args[0],
			Session: protocol.ScenarioSessionInput{
				Name:       name,
				Repo:       repo,
				Agent:      agent,
				Model:      model,
				Base:       base,
				Role:       role,
				Task:       task,
				AgentHooks: true,
			},
		})

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

		_ = c.SendControl("scenario_list", protocol.ScenarioListMsg{})

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
			_, _ = fmt.Fprintf(tw, "  NAME\tID\tSTATUS\tSESSIONS\tGOAL\n")

			for _, sc := range listResp.Scenarios {
				goal := sc.Goal
				if len(goal) > 60 {
					goal = goal[:57] + "..."
				}

				_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\n", sc.Name, sc.ID, sc.Status, len(sc.Sessions), goal)
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

// registerScenarioCmd registers this command on rootCmd. Called from registerCommands.
func registerScenarioCmd() {
	scenarioCmd.AddCommand(scenarioStartCmd)
	scenarioCmd.AddCommand(scenarioStopCmd)
	scenarioCmd.AddCommand(scenarioResumeCmd)
	scenarioCmd.AddCommand(scenarioDeleteCmd)
	scenarioCmd.AddCommand(scenarioStatusCmd)
	scenarioCmd.AddCommand(scenarioAddCmd)
	scenarioCmd.AddCommand(scenarioListCmd)

	scenarioAddCmd.Flags().String("name", "", "Session name (required)")
	scenarioAddCmd.Flags().String("repo", "", "Repository path (required)")
	scenarioAddCmd.Flags().String("agent", "", "Agent type")
	scenarioAddCmd.Flags().String("model", "", "Model override")
	scenarioAddCmd.Flags().String("role", "", "Session role")
	scenarioAddCmd.Flags().String("task", "", "Task/prompt")
	scenarioAddCmd.Flags().String("base", "", "Base branch")

	rootCmd.AddCommand(scenarioCmd)
}
