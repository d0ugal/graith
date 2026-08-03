package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func buildHelpTreeTestRoot(t *testing.T) *cobra.Command {
	t.Helper()

	root := &cobra.Command{
		Use:   "gr",
		Short: "Braw CLI",
	}
	root.PersistentFlags().Bool("json", false, "JSON output")

	braw := &cobra.Command{
		Use:     "braw [name]",
		Aliases: []string{"b"},
		Short:   "Run braw work",
		Run:     func(_ *cobra.Command, _ []string) {},
	}
	braw.Flags().Bool("dry-run", false, "preview changes")
	braw.Flags().Int("limit", 0, "maximum results")
	braw.Flags().StringP("output", "o", "text", "output format. One of: json, text")
	braw.Flags().String("token", "", "hidden token")

	if err := braw.Flags().MarkHidden("token"); err != nil {
		t.Fatalf("hide token flag: %v", err)
	}

	canny := &cobra.Command{
		Use:     "canny",
		Aliases: []string{"c"},
		Short:   "Group canny commands",
	}
	canny.PersistentFlags().String("scope", "", "scope for nested commands")

	dreich := &cobra.Command{
		Use:     "dreich <kind>",
		Aliases: []string{"d"},
		Short:   "Run dreich work",
		Run:     func(_ *cobra.Command, _ []string) {},
	}
	dreich.Flags().Duration("timeout", time.Second, "deadline")
	canny.AddCommand(dreich)

	secret := &cobra.Command{
		Use:    "secret",
		Short:  "Hidden command",
		Hidden: true,
		Run:    func(_ *cobra.Command, _ []string) {},
	}

	thrawn := &cobra.Command{
		Use:        "thrawn",
		Short:      "Deprecated command",
		Deprecated: "use braw",
		Run:        func(_ *cobra.Command, _ []string) {},
	}

	root.AddCommand(braw, canny, secret, thrawn)

	return root
}

func TestRenderHelpTree(t *testing.T) {
	t.Parallel()

	root := buildHelpTreeTestRoot(t)

	got := renderHelpTree(root, 0)
	want := strings.Join([]string{
		"gr — Braw CLI",
		"  braw [name] --dry-run --limit=INT -o=(json|text)",
		"  canny — Group canny commands",
		"    dreich <kind> --timeout=DUR --scope=STR",
		"",
	}, "\n")

	if got != want {
		t.Fatalf("renderHelpTree() =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderHelpTreeDepth(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		depth int
		want  string
	}{
		"one level": {
			depth: 1,
			want: strings.Join([]string{
				"gr — Braw CLI",
				"  braw [name] --dry-run --limit=INT -o=(json|text)",
				"  canny — Group canny commands",
				"",
			}, "\n"),
		},
		"unlimited": {
			depth: 0,
			want: strings.Join([]string{
				"gr — Braw CLI",
				"  braw [name] --dry-run --limit=INT -o=(json|text)",
				"  canny — Group canny commands",
				"    dreich <kind> --timeout=DUR --scope=STR",
				"",
			}, "\n"),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := buildHelpTreeTestRoot(t)

			if got := renderHelpTree(root, test.depth); got != test.want {
				t.Fatalf("renderHelpTree(depth=%d) =\n%s\nwant:\n%s", test.depth, got, test.want)
			}
		})
	}
}

func TestFindHelpTreeSubtree(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path      []string
		wantName  string
		wantFound bool
	}{
		"direct child":       {path: []string{"canny"}, wantName: "canny", wantFound: true},
		"direct alias":       {path: []string{"c"}, wantName: "canny", wantFound: true},
		"nested child":       {path: []string{"canny", "dreich"}, wantName: "dreich", wantFound: true},
		"nested aliases":     {path: []string{"c", "d"}, wantName: "dreich", wantFound: true},
		"hidden child":       {path: []string{"secret"}, wantFound: false},
		"deprecated child":   {path: []string{"thrawn"}, wantFound: false},
		"missing child":      {path: []string{"bothy"}, wantFound: false},
		"missing grandchild": {path: []string{"canny", "bothy"}, wantFound: false},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := buildHelpTreeTestRoot(t)

			got, found := findHelpTreeSubtree(root, test.path)
			if found != test.wantFound {
				t.Fatalf("findHelpTreeSubtree(%v) found = %v, want %v", test.path, found, test.wantFound)
			}

			if !found {
				return
			}

			if got.Name() != test.wantName {
				t.Fatalf("findHelpTreeSubtree(%v).Name() = %q, want %q", test.path, got.Name(), test.wantName)
			}
		})
	}
}

func TestFormatHelpTreeFlag(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "braw"}
	cmd.Flags().StringP("output", "o", "text", "output format. One of: json, text")
	cmd.Flags().Int("limit", 0, "maximum results")
	cmd.Flags().Bool("dry-run", false, "preview changes")
	cmd.Flags().Duration("timeout", time.Second, "deadline")
	cmd.Flags().StringSlice("label", nil, "labels")

	tests := map[string]string{
		"output":  "-o=(json|text)",
		"limit":   "--limit=INT",
		"dry-run": "--dry-run",
		"timeout": "--timeout=DUR",
		"label":   "--label=STR,...",
	}

	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			flag := cmd.Flags().Lookup(name)
			if flag == nil {
				t.Fatalf("flag %q not found", name)
			}

			if got := formatHelpTreeFlag(flag); got != want {
				t.Fatalf("formatHelpTreeFlag(%q) = %q, want %q", name, got, want)
			}
		})
	}
}

func TestHelpTreeCommandText(t *testing.T) {
	tests := map[string]struct {
		args           []string
		wantContains   []string
		wantNotContain []string
		wantErr        string
	}{
		"depth one": {
			args:           []string{"--depth", "1"},
			wantContains:   []string{"gr — Braw CLI", "  canny — Group canny commands"},
			wantNotContain: []string{"dreich", "secret", "thrawn"},
		},
		"subtree": {
			args:           []string{"canny"},
			wantContains:   []string{"canny — Group canny commands", "  dreich <kind> --timeout=DUR --scope=STR"},
			wantNotContain: []string{"braw"},
		},
		"subtree alias": {
			args:           []string{"c"},
			wantContains:   []string{"canny — Group canny commands", "  dreich <kind> --timeout=DUR --scope=STR"},
			wantNotContain: []string{"braw", "--json"},
		},
		"unknown subtree": {
			args:    []string{"canny", "bothy"},
			wantErr: "unknown command path \"canny bothy\"",
		},
		"negative depth": {
			args:    []string{"--depth", "-1"},
			wantErr: "--depth must be zero or positive",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := buildHelpTreeTestRoot(t)
			cmd := newHelpTreeCmd(root)

			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(test.args)

			err := cmd.Execute()
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Execute() error = %v, want substring %q", err, test.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			got := buf.String()
			for _, want := range test.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}

			for _, unwanted := range test.wantNotContain {
				if strings.Contains(got, unwanted) {
					t.Errorf("output contains %q:\n%s", unwanted, got)
				}
			}
		})
	}
}

func TestHelpTreeCommandJSON(t *testing.T) {
	origJSON := jsonOutput

	t.Cleanup(func() { jsonOutput = origJSON })

	jsonOutput = true

	root := buildHelpTreeTestRoot(t)
	cmd := newHelpTreeCmd(root)

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--depth", "1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var got helpTreeNode
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}

	if got.Name != "gr" || got.Short != "Braw CLI" {
		t.Fatalf("root = %+v, want gr with short text", got)
	}

	if len(got.Children) != 2 {
		t.Fatalf("children = %+v, want visible direct children only", got.Children)
	}

	if got.Children[0].Name != "braw" {
		t.Fatalf("first child = %+v, want braw", got.Children[0])
	}

	wantFlags := []string{"--dry-run", "--limit=INT", "-o=(json|text)"}
	if strings.Join(got.Children[0].Flags, " ") != strings.Join(wantFlags, " ") {
		t.Fatalf("braw flags = %v, want %v", got.Children[0].Flags, wantFlags)
	}

	if len(got.Children[1].Children) != 0 {
		t.Fatalf("depth 1 included grandchildren: %+v", got.Children[1].Children)
	}
}
