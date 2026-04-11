package cli

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dougalmatthews/graith/internal/client"
	"github.com/dougalmatthews/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var listRepo string

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("list", struct{}{})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(resp, &list); err != nil {
			return err
		}

		if listRepo != "" {
			filtered := list.Sessions[:0]
			for _, s := range list.Sessions {
				if s.RepoPath == listRepo || strings.HasSuffix(s.RepoPath, "/"+listRepo) || s.RepoName == listRepo {
					filtered = append(filtered, s)
				}
			}
			list.Sessions = filtered
		}

		if jsonOutput {
			return out.JSON(list)
		}

		if len(list.Sessions) == 0 {
			out.Print("No sessions. Create one with: gr new <name>\n")
			return nil
		}

		sort.Slice(list.Sessions, func(i, j int) bool {
			if list.Sessions[i].RepoName != list.Sessions[j].RepoName {
				return list.Sessions[i].RepoName < list.Sessions[j].RepoName
			}
			return list.Sessions[i].Name < list.Sessions[j].Name
		})

		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tREPO\tAGENT\tSTATUS")
		for _, s := range list.Sessions {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Name, s.RepoName, s.Agent, s.Status)
		}
		tw.Flush()

		return nil
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
	listCmd.Flags().StringVar(&listRepo, "repo", "", "filter by repo path")
}
