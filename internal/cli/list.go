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

var (
	listRepo     string
	listTree     bool
	listChildren string
	listStarred  bool
)

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

		if listChildren != "" {
			parent := findSession(list.Sessions, listChildren)
			if parent == nil {
				return fmt.Errorf("session %q not found", listChildren)
			}
			list.Sessions = descendantsOf(list.Sessions, parent.ID)
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

		if listStarred {
			filtered := list.Sessions[:0]
			for _, s := range list.Sessions {
				if s.Starred {
					filtered = append(filtered, s)
				}
			}
			list.Sessions = filtered
		}

		if jsonOutput {
			return out.JSON(list)
		}

		if paths.Profile != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Profile: %s\n\n", paths.Profile)
		}

		if len(list.Sessions) == 0 {
			out.Print("No sessions. Create one with: gr new <name>\n")
			return nil
		}

		now := time.Now()

		if listTree {
			printTree(cmd, list.Sessions, now)
		} else {
			printFlat(cmd, list.Sessions, now)
		}

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

func printFlat(cmd *cobra.Command, sessions []protocol.SessionInfo, now time.Time) {
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].RepoName != sessions[j].RepoName {
			return sessions[i].RepoName < sessions[j].RepoName
		}
		return sessions[i].Name < sessions[j].Name
	})

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tREPO\tAGENT\tSTATUS\tACTIVITY\tMODEL\tBRANCH\tGIT\tAGE\tATTACHED")
	for _, s := range sessions {
		name := s.Name
		if s.Starred {
			name = "★ " + name
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			name, s.RepoName, s.Agent, s.Status,
			formatAgentStatus(s), formatModel(s),
			formatBranch(s), formatGitStatus(s), formatAge(s, now), formatAttached(s, now))
	}
	tw.Flush()
}

func printTree(cmd *cobra.Command, sessions []protocol.SessionInfo, now time.Time) {
	byID := make(map[string]protocol.SessionInfo, len(sessions))
	children := make(map[string][]protocol.SessionInfo)
	var roots []protocol.SessionInfo

	for _, s := range sessions {
		byID[s.ID] = s
	}

	for _, s := range sessions {
		if s.ParentID == "" || byID[s.ParentID].ID == "" {
			roots = append(roots, s)
		} else {
			children[s.ParentID] = append(children[s.ParentID], s)
		}
	}

	sortSessions := func(ss []protocol.SessionInfo) {
		sort.Slice(ss, func(i, j int) bool {
			if ss[i].RepoName != ss[j].RepoName {
				return ss[i].RepoName < ss[j].RepoName
			}
			return ss[i].Name < ss[j].Name
		})
	}
	sortSessions(roots)
	for k := range children {
		sortSessions(children[k])
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tREPO\tAGENT\tSTATUS\tACTIVITY\tMODEL\tBRANCH\tGIT\tAGE\tATTACHED")

	var walk func(s protocol.SessionInfo, prefix, childPrefix string)
	walk = func(s protocol.SessionInfo, prefix, childPrefix string) {
		name := s.Name
		if s.Starred {
			name = "★ " + name
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			prefix, name, s.RepoName, s.Agent, s.Status,
			formatAgentStatus(s), formatModel(s),
			formatBranch(s), formatGitStatus(s), formatAge(s, now), formatAttached(s, now))

		kids := children[s.ID]
		for i, kid := range kids {
			if i == len(kids)-1 {
				walk(kid, childPrefix+"`-- ", childPrefix+"    ")
			} else {
				walk(kid, childPrefix+"|-- ", childPrefix+"|   ")
			}
		}
	}

	for _, root := range roots {
		walk(root, "", "")
	}
	tw.Flush()
}

func findSession(sessions []protocol.SessionInfo, nameOrID string) *protocol.SessionInfo {
	for i, s := range sessions {
		if s.Name == nameOrID || s.ID == nameOrID {
			return &sessions[i]
		}
	}
	return nil
}

func descendantsOf(sessions []protocol.SessionInfo, parentID string) []protocol.SessionInfo {
	children := make(map[string][]protocol.SessionInfo)
	for _, s := range sessions {
		if s.ParentID != "" {
			children[s.ParentID] = append(children[s.ParentID], s)
		}
	}

	var result []protocol.SessionInfo
	seen := map[string]bool{parentID: true}
	var collect func(string)
	collect = func(id string) {
		for _, child := range children[id] {
			if !seen[child.ID] {
				seen[child.ID] = true
				result = append(result, child)
				collect(child.ID)
			}
		}
	}
	collect(parentID)
	return result
}

func formatAgentStatus(s protocol.SessionInfo) string {
	agentStatus := s.AgentStatus
	if s.Status != "running" {
		agentStatus = ""
	}
	if agentStatus == "approval" {
		agentStatus = "⚠ approval"
	} else if agentStatus == "active" && s.ToolName != "" {
		agentStatus = fmt.Sprintf("active (%s)", s.ToolName)
	}
	return agentStatus
}

func formatModel(s protocol.SessionInfo) string {
	return s.Model
}

func formatBranch(s protocol.SessionInfo) string {
	branch := s.Branch
	if branch != "" {
		parts := strings.SplitN(branch, "/", 3)
		if len(parts) == 3 {
			branch = parts[2]
		}
	} else if s.InPlace {
		branch = "(in-place)"
	}
	return branch
}

func formatGitStatus(s protocol.SessionInfo) string {
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
	return gitStatus
}

func formatAge(s protocol.SessionInfo, now time.Time) string {
	if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
		return client.ShortDuration(now.Sub(t))
	}
	return ""
}

func formatAttached(s protocol.SessionInfo, now time.Time) string {
	if s.LastAttachedAt != "" {
		if t, err := time.Parse(time.RFC3339, s.LastAttachedAt); err == nil {
			return client.ShortDuration(now.Sub(t)) + " ago"
		}
	}
	return ""
}

func init() {
	rootCmd.AddCommand(listCmd)
	listCmd.Flags().StringVar(&listRepo, "repo", "", "filter by repo path")
	listCmd.Flags().BoolVar(&listTree, "tree", false, "show parent-child hierarchy")
	listCmd.Flags().StringVar(&listChildren, "children", "", "filter to descendants of a session")
	listCmd.Flags().BoolVar(&listStarred, "starred", false, "show only starred sessions")

	listCmd.RegisterFlagCompletionFunc("repo", completeRepoPaths)
	listCmd.RegisterFlagCompletionFunc("children", completeSessionNames)
}
