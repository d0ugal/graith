package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type helpTreeOptions struct {
	depth int
	root  *cobra.Command
}

type helpTreeNode struct {
	Name     string         `json:"name"`
	Short    string         `json:"short,omitempty"`
	Args     string         `json:"args,omitempty"`
	Flags    []string       `json:"flags,omitempty"`
	Children []helpTreeNode `json:"children,omitempty"`
}

var helpTreeEnumPattern = regexp.MustCompile(`(?i)\bone of:?\s+([a-z0-9_-]+(?:\s*,\s*[a-z0-9_-]+)*)`)

func newHelpTreeCmd(root *cobra.Command) *cobra.Command {
	opts := &helpTreeOptions{root: root}

	cmd := &cobra.Command{
		Use:   "help-tree [COMMAND...]",
		Short: "Print a compact command tree",
		Long: `Print a compact tree of gr commands.

Use positional command names to show only one subtree, for example:
  gr help-tree msg
  gr help-tree scenario start

Use --depth to limit nesting. A depth of 1 prints the selected command plus its
direct children; 0 means unlimited.`,
		Args: cobra.ArbitraryArgs,
		RunE: opts.run,
	}

	cmd.Flags().IntVar(&opts.depth, "depth", 0, "maximum nesting depth (1 = root + direct children, 0 = unlimited)")

	return cmd
}

func (o *helpTreeOptions) run(cmd *cobra.Command, args []string) error {
	if o.depth < 0 {
		return errors.New("--depth must be zero or positive")
	}

	target := o.root

	if len(args) > 0 {
		var ok bool

		target, ok = findHelpTreeSubtree(o.root, args)
		if !ok {
			path := strings.Join(args, " ")
			return fmt.Errorf("unknown command path %q; run `gr help-tree --depth 1` to list top-level commands", path)
		}
	}

	if jsonOutput {
		return writeHelpTreeJSON(cmd.OutOrStdout(), buildHelpTreeNode(target, 0, o.depth))
	}

	_, err := fmt.Fprint(cmd.OutOrStdout(), renderHelpTree(target, o.depth))

	return err
}

func renderHelpTree(root *cobra.Command, maxDepth int) string {
	var b strings.Builder

	renderHelpTreeNode(&b, root, 0, maxDepth)

	return b.String()
}

func renderHelpTreeNode(b *strings.Builder, cmd *cobra.Command, depth int, maxDepth int) {
	children := availableHelpTreeSubcommands(cmd)
	isLeaf := len(children) == 0

	line := strings.Repeat("  ", depth) + cmd.Name()
	if isLeaf {
		if args := helpTreeArgs(cmd); args != "" {
			line += " " + args
		}

		if flags := formatHelpTreeFlags(cmd); flags != "" {
			line += " " + flags
		}
	} else if cmd.Short != "" {
		line += " — " + cmd.Short
	}

	fmt.Fprintln(b, line)

	if maxDepth > 0 && depth >= maxDepth {
		return
	}

	for _, child := range children {
		renderHelpTreeNode(b, child, depth+1, maxDepth)
	}
}

func buildHelpTreeNode(cmd *cobra.Command, depth int, maxDepth int) helpTreeNode {
	children := availableHelpTreeSubcommands(cmd)
	isLeaf := len(children) == 0

	node := helpTreeNode{
		Name:  cmd.Name(),
		Short: cmd.Short,
	}

	if isLeaf {
		node.Args = helpTreeArgs(cmd)
		node.Flags = helpTreeFlagParts(cmd)
	}

	if maxDepth > 0 && depth >= maxDepth {
		return node
	}

	for _, child := range children {
		node.Children = append(node.Children, buildHelpTreeNode(child, depth+1, maxDepth))
	}

	return node
}

func writeHelpTreeJSON(w io.Writer, node helpTreeNode) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	return enc.Encode(node)
}

func availableHelpTreeSubcommands(cmd *cobra.Command) []*cobra.Command {
	children := make([]*cobra.Command, 0, len(cmd.Commands()))
	for _, child := range cmd.Commands() {
		if child.IsAvailableCommand() {
			children = append(children, child)
		}
	}

	return children
}

func findHelpTreeSubtree(root *cobra.Command, path []string) (*cobra.Command, bool) {
	current := root

	for _, name := range path {
		var match *cobra.Command

		for _, child := range availableHelpTreeSubcommands(current) {
			if child.Name() == name || child.HasAlias(name) {
				match = child
				break
			}
		}

		if match == nil {
			return nil, false
		}

		current = match
	}

	return current, true
}

func helpTreeArgs(cmd *cobra.Command) string {
	_, args, ok := strings.Cut(cmd.Use, " ")
	if !ok {
		return ""
	}

	return args
}

func formatHelpTreeFlags(cmd *cobra.Command) string {
	return strings.Join(helpTreeFlagParts(cmd), " ")
}

func helpTreeFlagParts(cmd *cobra.Command) []string {
	flags := make([]string, 0)
	seen := make(map[string]struct{})

	appendFlag := func(flag *pflag.Flag) {
		if flag.Name == "help" || flag.Hidden {
			return
		}

		if _, ok := seen[flag.Name]; ok {
			return
		}

		seen[flag.Name] = struct{}{}
		flags = append(flags, formatHelpTreeFlag(flag))
	}

	cmd.NonInheritedFlags().VisitAll(func(flag *pflag.Flag) {
		appendFlag(flag)
	})

	rootFlags := cmd.Root().PersistentFlags()
	cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if rootFlags.Lookup(flag.Name) != nil {
			return
		}

		appendFlag(flag)
	})

	return flags
}

func formatHelpTreeFlag(flag *pflag.Flag) string {
	name := "--" + flag.Name
	if flag.Shorthand != "" && flag.ShorthandDeprecated == "" {
		name = "-" + flag.Shorthand
	}

	switch flag.Value.Type() {
	case "bool", "count":
		return name
	}

	if values := detectHelpTreeEnum(flag.Usage); values != "" {
		return name + "=" + values
	}

	return name + "=" + helpTreeTypeName(flag.Value.Type())
}

func detectHelpTreeEnum(usage string) string {
	matches := helpTreeEnumPattern.FindStringSubmatch(usage)
	if len(matches) < 2 {
		return ""
	}

	parts := strings.Split(matches[1], ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	return "(" + strings.Join(parts, "|") + ")"
}

func helpTreeTypeName(flagType string) string {
	switch flagType {
	case "string":
		return "STR"
	case "stringArray", "stringSlice":
		return "STR,..."
	case "int", "int32", "int64", "uint", "uint32", "uint64":
		return "INT"
	case "float32", "float64":
		return "NUM"
	case "duration":
		return "DUR"
	default:
		return strings.ToUpper(flagType)
	}
}

func registerHelpTreeCmd() {
	rootCmd.AddCommand(newHelpTreeCmd(rootCmd))
}
