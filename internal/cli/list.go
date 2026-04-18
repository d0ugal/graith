package cli

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

var listRepo string

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		updateCh := make(chan *version.UpdateResult, 1)
		go func() {
			updateCh <- version.CheckForUpdate(paths.DataDir)
		}()

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

		now := time.Now()

		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tREPO\tAGENT\tSTATUS\tACTIVITY\tBRANCH\tGIT\tAGE\tATTACHED")
		for _, s := range list.Sessions {
			gitStatus := ""
			if s.Dirty {
				gitStatus = "dirty"
			}
			if s.UnpushedCount > 0 {
				if gitStatus != "" {
					gitStatus += ", "
				}
				gitStatus += fmt.Sprintf("%d ahead", s.UnpushedCount)
			}

			agentStatus := s.AgentStatus
			if s.Status != "running" {
				agentStatus = ""
			}
			if agentStatus == "approval" {
				agentStatus = "⚠ approval"
			}

			age := ""
			if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
				age = client.ShortDuration(now.Sub(t))
			}

			attached := ""
			if s.LastAttachedAt != "" {
				if t, err := time.Parse(time.RFC3339, s.LastAttachedAt); err == nil {
					attached = client.ShortDuration(now.Sub(t)) + " ago"
				}
			}

			branch := s.Branch
			if branch != "" {
				// Strip the common graith prefix to save space.
				parts := strings.SplitN(branch, "/", 3)
				if len(parts) == 3 {
					branch = parts[2]
				}
			}

			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				s.Name, s.RepoName, s.Agent, s.Status, agentStatus, branch, gitStatus, age, attached)
		}
		tw.Flush()

		select {
		case result := <-updateCh:
			if result != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "\nUpdate available: %s → %s (brew upgrade graith)\n",
					result.CurrentVersion, result.LatestVersion)
			}
		default:
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
	listCmd.Flags().StringVar(&listRepo, "repo", "", "filter by repo path")
}
