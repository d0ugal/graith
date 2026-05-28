package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var restartBackground bool

var restartCmd = &cobra.Command{
	Use:               "restart <name-or-id>",
	Short:             "Restart a stopped session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeStoppedSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		session, err := resolveSessionInfo(c, args[0])
		if err != nil {
			return err
		}
		if session.Status == "running" {
			return fmt.Errorf("session %q is already running", session.Name)
		}

		c.SendControl("resume", protocol.ResumeMsg{SessionID: session.ID})
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

		out.Print("Session %s restarted\n", info.Name)

		if restartBackground {
			return nil
		}

		return runAttachByID(c, info.ID, nil)
	},
}

func init() {
	rootCmd.AddCommand(restartCmd)
	restartCmd.Flags().BoolVar(&restartBackground, "background", false, "restart without attaching")
}

func completeStoppedSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer c.Close()

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, s := range list.Sessions {
		if s.Status == "stopped" {
			names = append(names, s.Name, s.ID)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
