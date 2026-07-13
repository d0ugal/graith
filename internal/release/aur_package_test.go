package release

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The goreleaserConfig type and loadGoreleaserConfig helper are shared across
// this package's release tests; they are defined in man_packaging_test.go.

// installRe pulls the source and destination out of an
// `install -Dm... "./src" "${pkgdir}/dst"` line in the AUR package() script.
// Capturing both (not just the source) lets tests assert where a file lands,
// so a source that installs to the wrong destination is still caught. It only
// matches quoted single-file installs (the completions, license, and binary);
// the man-tree glob install is checked separately in TestAURPackageShipsManTree.
var installRe = regexp.MustCompile(`install\s+-D\S*\s+"\./([^"]+)"\s+"([^"]+)"`)

// aurInstall is one single-file `install` invocation from the AUR package()
// script.
type aurInstall struct {
	src string // source path relative to the archive root (no leading ./)
	dst string // destination, e.g. ${pkgdir}/usr/share/licenses/graith-bin/LICENSE
}

// aurPackageInstalls returns the single-file install invocations in the AUR
// package() script. Parsing source and destination together means these tests
// match on real install lines, not on incidental substrings in comments.
func aurPackageInstalls(t *testing.T, script string) []aurInstall {
	t.Helper()

	var installs []aurInstall
	for _, m := range installRe.FindAllStringSubmatch(script, -1) {
		installs = append(installs, aurInstall{src: m[1], dst: m[2]})
	}

	if len(installs) == 0 {
		t.Fatal("no single-file `install` lines found in AUR package() script")
	}

	return installs
}

// TestAURPackageSourcesAreArchived is the regression test for issue #777: the
// AUR package() installed completions from paths like ./completions/gr.bash,
// but the release archive graith-bin builds from did not contain them because
// the archive had no `files:` list. Every single-file source the package()
// references must be present in the archive files, or the generated PKGBUILD
// fails at build time with "cannot stat". (The man tree is a glob and is
// checked in TestAURPackageShipsManTree.)
func TestAURPackageSourcesAreArchived(t *testing.T) {
	cfg := loadGoreleaserConfig(t)

	if len(cfg.Aurs) == 0 {
		t.Fatal("no aurs block in .goreleaser.yaml")
	}

	if len(cfg.Archives) == 0 {
		t.Fatal("no archives block in .goreleaser.yaml")
	}

	// The AUR -bin package builds from the Linux archive (aurs.ids: [linux]).
	linuxFiles := archiveByID(t, cfg, "linux")

	archived := make(map[string]bool)
	for _, f := range linuxFiles {
		archived[f.path()] = true
	}

	for _, inst := range aurPackageInstalls(t, cfg.Aurs[0].Package) {
		// `gr` is the built binary, injected by GoReleaser into the archive
		// automatically — it is never listed under archive files.
		if inst.src == "gr" {
			continue
		}

		if !archived[inst.src] {
			t.Errorf("AUR package() installs %q but it is not in the Linux archive files: %v",
				inst.src, linuxFiles)
		}
	}
}

// TestAURPackageShipsManTree guards the man page install the deb/rpm already
// ship: without it, Arch users get completions but no `man gr`. The man tree is
// shipped as a glob (gr.1.gz plus gr-*.1.gz subcommand pages, issue #779), so
// this asserts both halves — the archive carries man/*.1.gz and the AUR
// package() installs it into the man1 directory (issue #777).
func TestAURPackageShipsManTree(t *testing.T) {
	cfg := loadGoreleaserConfig(t)

	if len(cfg.Archives) == 0 {
		t.Fatal("no archives block in .goreleaser.yaml")
	}

	const manGlob = "man/*.1.gz"

	// The AUR -bin package builds from the Linux archive (aurs.ids: [linux]).
	linuxFiles := archiveByID(t, cfg, "linux")

	hasManGlob := slices.ContainsFunc(linuxFiles, func(f archiveFile) bool {
		return f.path() == manGlob
	})
	if !hasManGlob {
		t.Errorf("Linux archive files does not ship the man tree glob %q; found: %v", manGlob, linuxFiles)
	}

	pkg := cfg.Aurs[0].Package
	if !strings.Contains(pkg, "./man/*.1.gz") {
		t.Errorf("AUR package() does not install the man tree glob (./man/*.1.gz):\n%s", pkg)
	}

	if !strings.Contains(pkg, "/usr/share/man/man1") {
		t.Errorf("AUR package() does not install the man tree into /usr/share/man/man1:\n%s", pkg)
	}
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
