package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// goreleaserConfigPath returns the path to the repo-root .goreleaser.yaml,
// relative to this test file (internal/release).
func goreleaserConfigPath() string {
	return filepath.Join("..", "..", ".goreleaser.yaml")
}

// archiveFile models a single entry of an archive's `files:` list. GoReleaser
// accepts both a bare string (a path/glob) and a `{src, dst}` mapping (used for
// the Darwin GraithNotifier.app bundle, issue #1101), so this decodes either
// form; path() returns the archive-source path in both cases.
type archiveFile struct {
	Value string // bare-string form, e.g. "man/*.1.gz"
	Src   string // src of the {src, dst} mapping form
	Dst   string // dst of the {src, dst} mapping form
}

func (a *archiveFile) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&a.Value)
	}

	var m struct {
		Src string `yaml:"src"`
		Dst string `yaml:"dst"`
	}
	if err := node.Decode(&m); err != nil {
		return err
	}

	a.Src, a.Dst = m.Src, m.Dst

	return nil
}

// path returns the archive-source path this entry references — the bare-string
// value, or the src of the {src, dst} mapping form.
func (a archiveFile) path() string {
	if a.Value != "" {
		return a.Value
	}

	return a.Src
}

// goreleaserConfig is the slice of .goreleaser.yaml the release tests care
// about, shared across this package: the before-hooks (which gzip the generated
// man tree), the nfpm contents (which map files into the deb/rpm), the archive
// files (what the release tarball carries), and the AUR package() script (what
// the generated PKGBUILD installs). Everything else is ignored.
type goreleaserConfig struct {
	Before struct {
		Hooks []string `yaml:"hooks"`
	} `yaml:"before"`
	Nfpms []struct {
		Contents []struct {
			Src string `yaml:"src"`
			Dst string `yaml:"dst"`
		} `yaml:"contents"`
	} `yaml:"nfpms"`
	Archives []struct {
		ID    string        `yaml:"id"`
		Files []archiveFile `yaml:"files"`
	} `yaml:"archives"`
	Aurs []struct {
		Package string `yaml:"package"`
	} `yaml:"aurs"`
}

// archiveByID returns the archive with the given id. Archives are split by OS
// (issue #1101), so tests that care about a specific tarball (e.g. the Linux
// archive the AUR package builds from) select it by id rather than assuming an
// order.
func archiveByID(t *testing.T, cfg goreleaserConfig, id string) []archiveFile {
	t.Helper()

	for _, a := range cfg.Archives {
		if a.ID == id {
			return a.Files
		}
	}

	t.Fatalf("no archive with id %q in .goreleaser.yaml", id)

	return nil
}

func loadGoreleaserConfig(t *testing.T) goreleaserConfig {
	t.Helper()

	data, err := os.ReadFile(goreleaserConfigPath())
	if err != nil {
		t.Fatalf("reading .goreleaser.yaml: %v", err)
	}

	var cfg goreleaserConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parsing .goreleaser.yaml: %v", err)
	}

	return cfg
}

// TestManTreeIsGzippedAsAGlob locks in the fix for issue #779: the before-hook
// must gzip the whole generated man tree (man/*.1), not just the top-level
// man/gr.1. Shipping only gr.1 leaves its SEE ALSO cross-references (gr-attach,
// gr-msg, …) pointing at pages that were never installed.
func TestManTreeIsGzippedAsAGlob(t *testing.T) {
	cfg := loadGoreleaserConfig(t)

	// Match on the property that fixes #779 — a gzip hook that targets the whole
	// man tree glob (man/*.1) — independent of the compression flags, so
	// cosmetic flag changes (e.g. dropping -9, reordering to -nf9) don't trip a
	// misleading "no glob" failure.
	for _, hook := range cfg.Before.Hooks {
		if strings.Contains(hook, "gzip") && strings.Contains(hook, "man/*.1") {
			return
		}
		// Guard against a regression back to gzipping only the root page.
		if strings.Contains(hook, "gzip") && strings.Contains(hook, "man/gr.1") && !strings.Contains(hook, "man/*.1") {
			t.Fatalf("before-hook %q gzips only the root page; it must glob the whole man tree (man/*.1) so subcommand pages are shipped (#779)", hook)
		}
	}

	t.Fatalf("no before-hook gzips the man tree as a glob (gzip … man/*.1); found hooks: %v", cfg.Before.Hooks)
}

// TestManTreeIsPackagedAsAGlob locks in the other half of the #779 fix: nfpm
// must install every gzipped man page (man/*.1.gz) into the man1 directory, not
// just gr.1.gz.
func TestManTreeIsPackagedAsAGlob(t *testing.T) {
	cfg := loadGoreleaserConfig(t)

	if len(cfg.Nfpms) == 0 {
		t.Fatal("no nfpms package defined in .goreleaser.yaml")
	}

	for _, pkg := range cfg.Nfpms {
		for _, c := range pkg.Contents {
			if c.Src != "./man/*.1.gz" {
				continue
			}
			// A glob src must target the man1 *directory*, not a single file
			// name, or nfpm can't place multiple matches.
			if c.Dst != "/usr/share/man/man1/" {
				t.Fatalf("man glob src %q maps to dst %q; want the man1 directory %q", c.Src, c.Dst, "/usr/share/man/man1/")
			}

			return
		}
	}

	t.Fatalf("no nfpm contents entry ships the man tree glob %q into %q (#779)", "./man/*.1.gz", "/usr/share/man/man1/")
}
