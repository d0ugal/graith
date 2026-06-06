package cli

import (
	"fmt"
	"os"

	"github.com/dougalmatthews/graith/internal/client"
	"github.com/dougalmatthews/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	newAgent      string
	newBase       string
	newBackground bool
	newPrompt     string
)

var newCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a new agent session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cwd, _ := os.Getwd()
		agent := newAgent
		if agent == "" {
			agent = cfg.DefaultAgent
		}

		c, err := client.New(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		if err := c.Handshake(); err != nil {
			return err
		}
		c.ReadControlResponse()

		c.SendControl("create", protocol.CreateMsg{
			Name:     name,
			Agent:    agent,
			RepoPath: cwd,
			Base:     newBase,
			Prompt:   newPrompt,
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

		var info protocol.SessionInfo
		protocol.DecodePayload(resp, &info)

		if jsonOutput {
			return out.JSON(info)
		}

		out.Print("Created session %s (%s) in %s\n", info.Name, info.ID, info.WorktreePath)

		if newBackground {
			return nil
		}

		return runAttachByID(c, info.ID)
	},
}

func init() {
	rootCmd.AddCommand(newCmd)
	newCmd.Flags().StringVar(&newAgent, "agent", "", "agent to use")
	newCmd.Flags().StringVar(&newBase, "base", "", "base branch")
	newCmd.Flags().BoolVar(&newBackground, "background", false, "create without attaching")
	newCmd.Flags().StringVarP(&newPrompt, "prompt", "p", "", "initial prompt for the agent")
}
