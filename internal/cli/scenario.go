package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

type scenarioFile struct {
	Version  int                   `toml:"version"`
	Scenario scenarioFileMeta      `toml:"scenario"`
	Sessions []scenarioFileSession `toml:"sessions"`
}

type scenarioFileMeta struct {
	Name string `toml:"name"`
	Goal string `toml:"goal"`
}

type scenarioFileSession struct {
	Name       string `toml:"name"`
	Repo       string `toml:"repo"`
	Agent      string `toml:"agent"`
	Model      string `toml:"model"`
	Base       string `toml:"base"`
	Role       string `toml:"role"`
	Task       string `toml:"task"`
	AgentHooks *bool  `toml:"agent_hooks"`
}

var scenarioCmd = &cobra.Command{
	Use:   "scenario",
	Short: "Manage multi-session scenarios",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var scenarioStartCmd = &cobra.Command{
	Use:   "start <file>",
	Short: "Start a scenario from a TOML file (or - for stdin)",
	Long:  "Start a multi-session scenario. Only the orchestrator session can start scenarios.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var data []byte
		var err error

		source := args[0]
		if source == "-" {
			data, err = io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
		} else if strings.HasPrefix(source, "store:") {
			return fmt.Errorf("store: prefix not yet implemented — use a file path or stdin (-)")
		} else {
			data, err = os.ReadFile(source)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
		}

		var sf scenarioFile
		if err := toml.Unmarshal(data, &sf); err != nil {
			return fmt.Errorf("parse scenario TOML: %w", err)
		}

		if sf.Version != 1 {
			return fmt.Errorf("unsupported scenario version %d (expected 1)", sf.Version)
		}
		if sf.Scenario.Name == "" {
			return fmt.Errorf("scenario.name is required")
		}
		if len(sf.Sessions) == 0 {
			return fmt.Errorf("at least one [[sessions]] entry is required")
		}

		sessions := make([]protocol.ScenarioSessionInput, len(sf.Sessions))
		for i, s := range sf.Sessions {
			if s.Name == "" {
				return fmt.Errorf("session %d: name is required", i)
			}
			if s.Repo == "" {
				return fmt.Errorf("session %q: repo is required", s.Name)
			}
			repo := config.ExpandPath(s.Repo)
			sessions[i] = protocol.ScenarioSessionInput{
				Name:       s.Name,
				Repo:       repo,
				Agent:      s.Agent,
				Model:      s.Model,
				Base:       s.Base,
				Role:       s.Role,
				Task:       s.Task,
				AgentHooks: s.AgentHooks == nil || *s.AgentHooks,
			}
		}

		callerID := os.Getenv("GRAITH_SESSION_ID")
		if callerID == "" {
			return fmt.Errorf("GRAITH_SESSION_ID is not set — scenarios must be started from within a graith session")
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("scenario_start", protocol.ScenarioStartMsg{
			CallerSessionID: callerID,
			Name:            sf.Scenario.Name,
			Goal:            sf.Scenario.Goal,
			Sessions:        sessions,
		})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		var record protocol.ScenarioRecord
		protocol.DecodePayload(resp, &record)

		if jsonOutput {
			return out.JSON(record)
		}

		out.Print("Scenario %q started (id: %s)\n", record.Name, record.ID)
		for _, s := range record.Sessions {
			out.Print("  %s (%s) — %s\n", s.Name, s.SessionID, s.Role)
		}
		return nil
	},
}

var scenarioStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop all sessions in a scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("scenario_stop", protocol.ScenarioStopMsg{Name: args[0]})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		if jsonOutput {
			return out.JSON(resp.Payload)
		}

		var result struct {
			Stopped []string `json:"stopped"`
		}
		protocol.DecodePayload(resp, &result)
		out.Print("Stopped %d sessions in scenario %q\n", len(result.Stopped), args[0])
		return nil
	},
}

var scenarioDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a scenario and all its sessions",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("scenario_delete", protocol.ScenarioDeleteMsg{Name: args[0]})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		if jsonOutput {
			return out.JSON(resp.Payload)
		}

		var result struct {
			Deleted []string `json:"deleted"`
		}
		protocol.DecodePayload(resp, &result)
		out.Print("Deleted scenario %q (%d sessions removed)\n", args[0], len(result.Deleted))
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

		c.SendControl("scenario_status", protocol.ScenarioStatusMsg{Name: args[0]})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		var statusResp protocol.ScenarioStatusResponse
		protocol.DecodePayload(resp, &statusResp)

		if jsonOutput {
			return out.JSON(statusResp)
		}

		sc := statusResp.Scenario
		out.Print("Scenario: %s (%s) — %s\n", sc.Name, sc.ID, sc.Status)
		out.Print("Goal: %s\n\n", sc.Goal)

		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintf(tw, "NAME\tSESSION\tSTATUS\tAGENT\tROLE\n")
		for _, s := range sc.Sessions {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.SessionID, s.Status, s.Agent, s.Role)
		}
		tw.Flush()
		return nil
	},
}

var scenarioListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all scenarios",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("scenario_list", protocol.ScenarioListMsg{})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		var listResp protocol.ScenarioListResponse
		protocol.DecodePayload(resp, &listResp)

		if jsonOutput {
			return out.JSON(listResp)
		}

		if len(listResp.Scenarios) == 0 {
			out.Print("No scenarios\n")
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintf(tw, "NAME\tID\tSTATUS\tSESSIONS\tGOAL\n")
		for _, sc := range listResp.Scenarios {
			goal := sc.Goal
			if len(goal) > 60 {
				goal = goal[:57] + "..."
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", sc.Name, sc.ID, sc.Status, len(sc.Sessions), goal)
		}
		tw.Flush()
		return nil
	},
}

func init() {
	scenarioCmd.AddCommand(scenarioStartCmd)
	scenarioCmd.AddCommand(scenarioStopCmd)
	scenarioCmd.AddCommand(scenarioDeleteCmd)
	scenarioCmd.AddCommand(scenarioStatusCmd)
	scenarioCmd.AddCommand(scenarioListCmd)
	rootCmd.AddCommand(scenarioCmd)
}
