package release

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// goreleaserConfigPath returns the path to the repo-root .goreleaser.yaml,
// relative to this test file (internal/release).
func goreleaserConfigPath() string {
	return filepath.Join("..", "..", ".goreleaser.yaml")
}

// goreleaserConfig is the slice of .goreleaser.yaml this test cares about: the
// archive `files` list (what the source tarball actually carries) and the AUR
// `package()` install script (what the generated PKGBUILD tries to install).
type goreleaserConfig struct {
	Archives []struct {
		Files []string `yaml:"files"`
	} `yaml:"archives"`
	Aurs []struct {
		Package string `yaml:"package"`
	} `yaml:"aurs"`
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

// installRe pulls the source and destination out of an
// `install -Dm... "./src" "${pkgdir}/dst"` line in the AUR package() script.
// Capturing both (not just the source) lets tests assert where a file lands,
// so a source that installs to the wrong destination is still caught.
var installRe = regexp.MustCompile(`install\s+-D\S*\s+"\./([^"]+)"\s+"([^"]+)"`)

// aurInstall is one `install` invocation from the AUR package() script.
type aurInstall struct {
	src string // source path relative to the archive root (no leading ./)
	dst string // destination, e.g. ${pkgdir}/usr/share/man/man1/gr.1.gz
}

// aurPackageInstalls returns the install invocations in the AUR package()
// script. Parsing source and destination together means these tests match on
// the real install lines, not on incidental substrings in comments.
func aurPackageInstalls(t *testing.T, script string) []aurInstall {
	t.Helper()

	var installs []aurInstall
	for _, m := range installRe.FindAllStringSubmatch(script, -1) {
		installs = append(installs, aurInstall{src: m[1], dst: m[2]})
	}

	if len(installs) == 0 {
		t.Fatal("no `install` lines found in AUR package() script")
	}

	return installs
}

// TestAURPackageSourcesAreArchived is the regression test for issue #777: the
// AUR package() installed completions (and, once fixed, a man page) from paths
// like ./completions/gr.bash, but the release archive graith-bin builds from
// did not contain them because the archive had no `files:` list. Every
// non-binary source the package() references must be present in the archive
// files, or the generated PKGBUILD fails at build time with "cannot stat".
func TestAURPackageSourcesAreArchived(t *testing.T) {
	cfg := loadGoreleaserConfig(t)

	if len(cfg.Aurs) == 0 {
		t.Fatal("no aurs block in .goreleaser.yaml")
	}

	if len(cfg.Archives) == 0 {
		t.Fatal("no archives block in .goreleaser.yaml")
	}

	archived := make(map[string]bool)
	for _, f := range cfg.Archives[0].Files {
		archived[f] = true
	}

	for _, inst := range aurPackageInstalls(t, cfg.Aurs[0].Package) {
		// `gr` is the built binary, injected by GoReleaser into the archive
		// automatically — it is never listed under archive files.
		if inst.src == "gr" {
			continue
		}

		if !archived[inst.src] {
			t.Errorf("AUR package() installs %q but it is not in the archive files: %v",
				inst.src, cfg.Archives[0].Files)
		}
	}
}

// TestAURPackageInstallsManPage guards the man page install the deb/rpm already
// ship: without it, Arch users get completions but no `man gr`. It asserts the
// exact source→destination pair, so a bare mention of the path in a comment (or
// an install to the wrong dir) does not satisfy it (issue #777).
func TestAURPackageInstallsManPage(t *testing.T) {
	cfg := loadGoreleaserConfig(t)

	const (
		wantSrc = "man/gr.1.gz"
		wantDst = "/usr/share/man/man1/gr.1.gz"
	)

	for _, inst := range aurPackageInstalls(t, cfg.Aurs[0].Package) {
		if inst.src == wantSrc {
			if !strings.HasSuffix(inst.dst, wantDst) {
				t.Errorf("AUR package() installs the man page to %q, want it under %q", inst.dst, wantDst)
			}

			return
		}
	}

	t.Errorf("AUR package() does not install the man page (%s):\n%s", wantSrc, cfg.Aurs[0].Package)
}

// TestAURLicensePathUsesPkgname locks in the Arch convention that a package's
// license lives under /usr/share/licenses/<pkgname>/, where pkgname is
// graith-bin, not the upstream project name graith (issue #777).
func TestAURLicensePathUsesPkgname(t *testing.T) {
	cfg := loadGoreleaserConfig(t)

	if strings.Contains(cfg.Aurs[0].Package, "usr/share/licenses/graith/") {
		t.Errorf("AUR package() installs the license under graith/ but pkgname is graith-bin:\n%s", cfg.Aurs[0].Package)
	}

	if !strings.Contains(cfg.Aurs[0].Package, "usr/share/licenses/graith-bin/LICENSE") {
		t.Errorf("AUR package() does not install the license under graith-bin/:\n%s", cfg.Aurs[0].Package)
	}
}
