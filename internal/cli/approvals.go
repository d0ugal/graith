package cli

import (
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var approvalsCmd = &cobra.Command{
	Use:   "approvals",
	Short: "List sessions waiting for approval",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("list", struct{}{})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(resp, &list); err != nil {
			return err
		}

		var waiting []protocol.SessionInfo

		for _, s := range list.Sessions {
			if s.Status == "running" && s.AgentStatus == "approval" {
				waiting = append(waiting, s)
			}
		}

		if jsonOutput {
			return out.JSON(protocol.SessionListMsg{Sessions: waiting})
		}

		if len(waiting) == 0 {
			out.Printf("No sessions waiting for approval.\n")
			return nil
		}

		sort.Slice(waiting, func(i, j int) bool {
			return waiting[i].Name < waiting[j].Name
		})

		now := time.Now()

		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tREPO\tAGENT\tAGE")

		for _, s := range waiting {
			age := ""
			if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
				age = client.ShortDuration(now.Sub(t))
			}

			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.RepoName, s.Agent, age)
		}

		_ = tw.Flush()

		return nil
	},
}

// registerApprovalsCmd registers this command on rootCmd. Called from registerCommands.
func registerApprovalsCmd() {
	rootCmd.AddCommand(approvalsCmd)
}
