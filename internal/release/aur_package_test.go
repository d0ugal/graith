package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var aurInstallRE = regexp.MustCompile(`install\s+-D\S*\s+"\\?\$\{srcdir\}/([^"]+)"\s+"\\?\$\{pkgdir\}(/[^"]+)"`)

func renderedAURFiles(t *testing.T, version string) (string, string) {
	t.Helper()

	work := t.TempDir()
	checksums := filepath.Join(work, "checksums.txt")

	lines := []string{
		strings.Repeat("a", 64) + "  graith_" + version + "_linux_amd64.tar.gz",
		strings.Repeat("b", 64) + "  graith_" + version + "_linux_arm64.tar.gz",
	}
	if err := os.WriteFile(checksums, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(releaseRootPath("scripts", "render-stable-aur.sh"), version, checksums, work)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("render AUR package: %v: %s", err, output)
	}

	pkgbuild, err := os.ReadFile(filepath.Join(work, "PKGBUILD"))
	if err != nil {
		t.Fatal(err)
	}

	srcinfo, err := os.ReadFile(filepath.Join(work, ".SRCINFO"))
	if err != nil {
		t.Fatal(err)
	}

	return string(pkgbuild), string(srcinfo)
}

func renderedAURPackage(t *testing.T) string {
	t.Helper()

	pkgbuild, _ := renderedAURFiles(t, "0.70.0")

	return pkgbuild
}

func TestAURPackageUsesVerifiedStableLinuxArchives(t *testing.T) {
	pkg := renderedAURPackage(t)
	for _, required := range []string{
		"graith_0.70.0_linux_amd64.tar.gz::https://github.com/d0ugal/graith/releases/download/v0.70.0/graith_0.70.0_linux_amd64.tar.gz",
		"graith_0.70.0_linux_arm64.tar.gz::https://github.com/d0ugal/graith/releases/download/v0.70.0/graith_0.70.0_linux_arm64.tar.gz",
		strings.Repeat("a", 64), strings.Repeat("b", 64),
	} {
		if !strings.Contains(pkg, required) {
			t.Errorf("rendered PKGBUILD missing %q", required)
		}
	}
}

func TestAURPackageInstallsReleasePayloadAndNativeEvidence(t *testing.T) {
	pkg := renderedAURPackage(t)
	for _, required := range []string{
		`${srcdir}/gr`, `${pkgdir}/usr/bin/gr`,
		`${srcdir}/libghostty-native.spdx.json`, `${pkgdir}/usr/share/doc/graith/libghostty-native.spdx.json`,
		`${srcdir}/THIRD_PARTY_NOTICES.libghostty.md`, `${pkgdir}/usr/share/doc/graith/THIRD_PARTY_NOTICES.libghostty.md`,
		`${srcdir}/completions/gr.bash`, `${srcdir}/completions/gr.zsh`, `${srcdir}/completions/gr.fish`,
		`${srcdir}"/man/*.1.gz`, `${pkgdir}/usr/share/man/man1/`,
		"options=('!strip')",
	} {
		if !strings.Contains(pkg, required) {
			t.Errorf("rendered PKGBUILD missing %q", required)
		}
	}

	if !aurInstallRE.MatchString(pkg) {
		t.Fatal("rendered PKGBUILD contains no recognized installed payload")
	}
}

func TestAURLicensePathUsesPkgname(t *testing.T) {
	pkg := renderedAURPackage(t)
	if strings.Contains(pkg, "usr/share/licenses/graith/LICENSE") {
		t.Fatal("AUR license is installed under graith instead of graith-bin")
	}

	if !strings.Contains(pkg, "usr/share/licenses/graith-bin/LICENSE") {
		t.Fatal("AUR license is not installed under the package name")
	}
}

func TestAURPackageNormalizesPrereleasePkgver(t *testing.T) {
	t.Parallel()

	pkgbuild, srcinfo := renderedAURFiles(t, "0.70.0-rc.1")
	for name, data := range map[string]string{
		"PKGBUILD": pkgbuild,
		".SRCINFO": srcinfo,
	} {
		if strings.Contains(data, "pkgver=0.70.0-rc.1") ||
			strings.Contains(data, "pkgver = 0.70.0-rc.1") {
			t.Errorf("%s retains a hyphen in pkgver", name)
		}

		if !strings.Contains(data, "pkgver=0.70.0_rc.1") &&
			!strings.Contains(data, "pkgver = 0.70.0_rc.1") {
			t.Errorf("%s does not normalize the prerelease pkgver", name)
		}

		for _, required := range []string{
			"graith_0.70.0-rc.1_linux_amd64.tar.gz",
			"graith_0.70.0-rc.1_linux_arm64.tar.gz",
			"/download/v0.70.0-rc.1/",
		} {
			if !strings.Contains(data, required) {
				t.Errorf("%s does not retain upstream release version in %q", name, required)
			}
		}
	}
}
