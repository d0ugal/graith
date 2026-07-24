package libghosttydeps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxDependencyDownload = 256 << 20

const dependencyBaseRefEnv = "GRAITH_LIBGHOSTTY_BASE_REF"

var httpClient = &http.Client{Timeout: 2 * time.Minute}

type moduleDownload struct {
	Version string `json:"Version"`
	Dir     string `json:"Dir"`
	Sum     string `json:"Sum"`
	Origin  struct {
		Hash string `json:"Hash"`
	} `json:"Origin"`
}

type zigIndexEntry struct {
	Tarball string `json:"tarball"`
	Shasum  string `json:"shasum"`
}

type zigRelease struct {
	Source     zigIndexEntry `json:"src"`
	LinuxX8664 zigIndexEntry `json:"x86_64-linux"`
}

type githubRelease struct {
	Body   string `json:"body"`
	Assets []struct {
		Name               string `json:"name"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

var generatedUnitFiles = []string{
	LockFilename,
	SPDXFilename,
	NoticesFilename,
	"go.mod",
	"go.sum",
	"gui/shared/Package.swift",
}

const generatedHeaders = "gui/shared/Sources/CGhosttyVT/include/ghostty"

func Generate(ctx context.Context, root string) error {
	lockPath := filepath.Join(root, LockFilename)

	// Renovate changes canonical versions and commits before this command runs,
	// so derived URLs may temporarily describe the previous unit. Generation
	// still requires a structurally complete lock and writes only a fully
	// consistent result; normal verification continues to reject this drift.
	lock, err := loadLockForGeneration(lockPath)
	if err != nil {
		return err
	}

	if err := validateRepositories(lock); err != nil {
		return err
	}

	primaryUpdate, err := primaryDependencyChanged(ctx, root, lock)
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "graith-libghostty-deps-")
	if err != nil {
		return fmt.Errorf("create native dependency work directory: %w", err)
	}

	defer func() {
		_ = os.RemoveAll(work)
	}()

	wrapperMoved, err := refreshGoLibghostty(ctx, root, &lock)
	if err != nil {
		return err
	}

	ghosttySource := filepath.Join(work, "ghostty")
	if err := checkoutCommit(ctx, lock.Ghostty.Repository, lock.Ghostty.Commit, ghosttySource); err != nil {
		return err
	}

	if err := refreshGhosttyClosure(ctx, ghosttySource, &lock, primaryUpdate || wrapperMoved); err != nil {
		return err
	}

	if err := refreshZig(ctx, &lock); err != nil {
		return err
	}

	if err := refreshSPDXTools(ctx, &lock); err != nil {
		return err
	}

	if err := refreshAppleArtifact(ctx, &lock); err != nil {
		return err
	}

	return withGeneratedUnitRollback(root, func() error {
		if err := updateGoModule(ctx, root, lock.GoLibghostty.Version); err != nil {
			return err
		}

		if err := synchronizeHeaders(ghosttySource, root, &lock); err != nil {
			return err
		}

		if err := WriteLock(lockPath, lock); err != nil {
			return err
		}

		if err := writeGeneratedMetadata(root, lock); err != nil {
			return err
		}

		if err := updateSwiftPackage(root, lock); err != nil {
			return err
		}

		if err := VerifyGenerated(root); err != nil {
			return fmt.Errorf("verify generated native dependency unit: %w", err)
		}

		return nil
	})
}

func validateRepositories(lock Lock) error {
	want := map[string][2]string{
		"go-libghostty": {lock.GoLibghostty.Repository, "https://tangled.org/mitchellh.com/go-libghostty"},
		"Ghostty":       {lock.Ghostty.Repository, "https://github.com/ghostty-org/ghostty.git"},
		"Zig":           {lock.Zig.Repository, "ziglang/zig"},
		"uucode":        {lock.Uucode.Repository, "jacobsandlund/uucode"},
		"Highway":       {lock.Highway.Repository, "google/highway"},
		"simdutf":       {lock.Simdutf.Repository, "simdutf/simdutf"},
		"SPDX tools":    {lock.SPDXTools.Repository, "spdx/tools-java"},
	}

	var problems []error

	for name, pair := range want {
		if pair[0] != pair[1] {
			problems = append(problems, fmt.Errorf("unexpected %s repository %q", name, pair[0]))
		}
	}

	return errors.Join(problems...)
}

func refreshGhosttyClosure(ctx context.Context, source string, lock *Lock, deriveVersions bool) error {
	rootManifest, err := os.ReadFile(filepath.Join(source, "build.zig.zon"))
	if err != nil {
		return fmt.Errorf("read Ghostty root manifest: %w", err)
	}

	ghosttyVersion, err := capture(rootManifest, `(?m)^\s*\.version = "([^"]+)"`)
	if err != nil {
		return fmt.Errorf("Ghostty version: %w", err)
	}

	zigVersion, err := capture(rootManifest, `(?m)^\s*\.minimum_zig_version = "([^"]+)"`)
	if err != nil {
		return fmt.Errorf("Ghostty Zig version: %w", err)
	}

	if err := reconcileVersion("Zig", zigVersion, &lock.Zig.Version, deriveVersions); err != nil {
		return err
	}

	lock.Ghostty.Version = ghosttyVersion

	lock.Ghostty.LicenseSHA256, err = fileSHA256(filepath.Join(source, "LICENSE"))
	if err != nil {
		return err
	}

	uucodeBlock, err := capture(rootManifest, `(?s)\.uucode\s*=\s*\.\{(.*?)\n\s*\},`)
	if err != nil {
		return fmt.Errorf("Ghostty uucode dependency: %w", err)
	}

	uucodeURL, err := capture([]byte(uucodeBlock), `\.url\s*=\s*"([^"]+)"`)
	if err != nil {
		return err
	}

	uucodeHash, err := capture([]byte(uucodeBlock), `\.hash\s*=\s*"([^"]+)"`)
	if err != nil {
		return err
	}

	uucodeVersion, err := capture([]byte(uucodeHash), `^uucode-([0-9][^-]*)-`)
	if err != nil {
		return fmt.Errorf("parse uucode version from Zig hash: %w", err)
	}

	if err := reconcileVersion("uucode", uucodeVersion, &lock.Uucode.Version, deriveVersions); err != nil {
		return err
	}

	if err := requireDependencyURL(uucodeURL, "uucode-"); err != nil {
		return err
	}

	lock.Uucode.SourceURL = uucodeURL
	lock.Uucode.ZigHash = uucodeHash

	uucodeArchive, err := download(ctx, uucodeURL)
	if err != nil {
		return fmt.Errorf("download uucode source: %w", err)
	}

	lock.Uucode.ArchiveSHA256 = bytesSHA256(uucodeArchive)
	for suffix, destination := range map[string]*string{
		"/LICENSE.md":                        &lock.Uucode.LicenseSHA256,
		"/licenses/LICENSE_Bjoern_Hoehrmann": &lock.Uucode.DecoderNoticeSHA256,
		"/licenses/LICENSE_unicode":          &lock.Uucode.UnicodeNoticeSHA256,
	} {
		content, archiveErr := tarGzipMember(uucodeArchive, suffix)
		if archiveErr != nil {
			return archiveErr
		}

		*destination = bytesSHA256(content)
	}

	highwayManifest, err := os.ReadFile(filepath.Join(source, "pkg/highway/build.zig.zon"))
	if err != nil {
		return fmt.Errorf("read Ghostty Highway manifest: %w", err)
	}

	highwayVersion, err := capture(highwayManifest, `(?m)^\s*\.version = "([^"]+)"`)
	if err != nil {
		return err
	}

	if err := reconcileVersion("Highway", highwayVersion, &lock.Highway.Version, deriveVersions); err != nil {
		return err
	}

	highwayURL, err := capture(highwayManifest, `\.url\s*=\s*"([^"]*highway-[0-9a-f]{40}\.tar\.gz)"`)
	if err != nil {
		return err
	}

	highwayCommit, err := capture(highwayManifest, `highway-([0-9a-f]{40})\.tar\.gz`)
	if err != nil {
		return err
	}

	highwayHash, err := capture(highwayManifest, `\.hash\s*=\s*"([^"]+)"`)
	if err != nil {
		return err
	}

	if err := requireDependencyURL(highwayURL, "highway-"); err != nil {
		return err
	}

	lock.Highway.Commit = highwayCommit
	lock.Highway.SourceURL = highwayURL
	lock.Highway.ZigHash = highwayHash

	highwayArchive, err := download(ctx, highwayURL)
	if err != nil {
		return fmt.Errorf("download Highway source: %w", err)
	}

	lock.Highway.ArchiveSHA256 = bytesSHA256(highwayArchive)

	highwayLicense, err := tarGzipMember(highwayArchive, "/LICENSE-BSD3")
	if err != nil {
		return err
	}

	lock.Highway.LicenseSHA256 = bytesSHA256(highwayLicense)

	simdutfManifest, err := os.ReadFile(filepath.Join(source, "pkg/simdutf/build.zig.zon"))
	if err != nil {
		return fmt.Errorf("read Ghostty simdutf manifest: %w", err)
	}

	lock.Simdutf.ManifestVersion, err = capture(simdutfManifest, `(?m)^\s*\.version = "([^"]+)"`)
	if err != nil {
		return err
	}

	simdutfHeader := filepath.Join(source, "pkg/simdutf/vendor/simdutf.h")

	header, err := os.ReadFile(simdutfHeader)
	if err != nil {
		return fmt.Errorf("read vendored simdutf header: %w", err)
	}

	compiledVersion, err := capture(header, `#define SIMDUTF_VERSION "([^"]+)"`)
	if err != nil {
		return err
	}

	if err := reconcileVersion("simdutf", compiledVersion, &lock.Simdutf.Version, deriveVersions); err != nil {
		return err
	}

	lock.Simdutf.HeaderSHA256 = bytesSHA256(header)

	lock.Simdutf.CppSHA256, err = fileSHA256(filepath.Join(source, "pkg/simdutf/vendor/simdutf.cpp"))
	if err != nil {
		return err
	}

	lock.Simdutf.Commit, err = resolveGitHubTag(ctx, lock.Simdutf.Repository, "v"+lock.Simdutf.Version)
	if err != nil {
		return fmt.Errorf("resolve simdutf release commit: %w", err)
	}

	simdutfLicense, err := download(ctx, fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/LICENSE-MIT", lock.Simdutf.Repository, lock.Simdutf.Commit))
	if err != nil {
		return fmt.Errorf("download simdutf license: %w", err)
	}

	lock.Simdutf.LicenseSHA256 = bytesSHA256(simdutfLicense)

	return nil
}

func refreshGoLibghostty(ctx context.Context, root string, lock *Lock) (bool, error) {
	output, err := run(ctx, root, "go", "mod", "download", "-json", "go.mitchellh.com/libghostty@"+lock.GoLibghostty.Commit)
	if err != nil {
		return false, err
	}

	var module moduleDownload
	if err := json.Unmarshal(output, &module); err != nil {
		return false, fmt.Errorf("decode go-libghostty module metadata: %w", err)
	}

	if module.Origin.Hash != lock.GoLibghostty.Commit {
		return false, fmt.Errorf("go-libghostty module origin = %s, want %s", module.Origin.Hash, lock.GoLibghostty.Commit)
	}

	lock.GoLibghostty.Version = module.Version
	lock.GoLibghostty.ModuleSum = module.Sum

	lock.GoLibghostty.LicenseSHA256, err = fileSHA256(filepath.Join(module.Dir, "LICENSE"))
	if err != nil {
		return false, err
	}

	cmake, err := os.ReadFile(filepath.Join(module.Dir, "CMakeLists.txt"))
	if err != nil {
		return false, fmt.Errorf("read go-libghostty CMake dependency declaration: %w", err)
	}

	testedGhostty, err := capture(cmake, `(?m)^\s*GIT_TAG\s+([0-9a-f]{40})\s*$`)
	if err != nil {
		return false, fmt.Errorf("resolve go-libghostty's tested Ghostty commit: %w", err)
	}

	wrapperMoved := followWrapperGhosttyPin(lock, testedGhostty)

	return wrapperMoved, nil
}

func followWrapperGhosttyPin(lock *Lock, testedGhostty string) bool {
	if lock.GoLibghostty.TestedGhosttyCommit == testedGhostty {
		return false
	}
	// A wrapper update moves its own exact Ghostty test pin. Use that tested
	// pair as the default update rather than independently guessing a newer C
	// ABI. A separately approved Ghostty-only proposal can still advance the
	// selected commit while the native compatibility suite reviews it.
	lock.Ghostty.Commit = testedGhostty
	lock.GoLibghostty.TestedGhosttyCommit = testedGhostty

	return true
}

func updateGoModule(ctx context.Context, root, version string) error {
	if _, err := run(ctx, root, "go", "mod", "edit", "-require=go.mitchellh.com/libghostty@"+version); err != nil {
		return err
	}

	if _, err := run(ctx, root, "go", "mod", "tidy"); err != nil {
		return err
	}

	return nil
}

func primaryDependencyChanged(ctx context.Context, root string, current Lock) (bool, error) {
	ref := os.Getenv(dependencyBaseRefEnv)

	required := ref != ""
	if ref == "" {
		ref = "HEAD"
	}

	if required {
		if _, err := run(ctx, root, "git", "rev-parse", "--verify", ref+"^{commit}"); err != nil {
			return false, fmt.Errorf("resolve native dependency base %s: %w", ref, err)
		}

		mergeBase, err := run(ctx, root, "git", "merge-base", ref, "HEAD")
		if err != nil {
			return false, fmt.Errorf("find native dependency merge base for %s: %w", ref, err)
		}

		ref = strings.TrimSpace(string(mergeBase))
		if !fullSHAPattern.MatchString(ref) {
			return false, fmt.Errorf("invalid native dependency merge base %q", ref)
		}
	}

	output, err := run(ctx, root, "git", "show", ref+":"+LockFilename)
	if err != nil {
		if required {
			// The base for the change that introduces the canonical lock has no
			// file to compare. Treat bootstrap as a primary update so Ghostty's
			// own declarations populate every transitive version.
			return true, nil
		}
		// A newly introduced, uncommitted lock has no HEAD projection. This is
		// only the bootstrap case; normal Renovate and regeneration runs always
		// have either HEAD or an explicit base revision available.
		return false, nil
	}

	previous, err := decodeLock(output, false)
	if err != nil {
		return false, fmt.Errorf("decode native dependency base %s: %w", ref, err)
	}

	return primaryDependencyDiffers(previous, current), nil
}

func primaryDependencyDiffers(previous, current Lock) bool {
	return previous.GoLibghostty.Commit != current.GoLibghostty.Commit ||
		previous.Ghostty.Commit != current.Ghostty.Commit
}

func reconcileVersion(component, compiled string, selected *string, derive bool) error {
	if derive {
		*selected = compiled
		return nil
	}

	if compiled != *selected {
		return fmt.Errorf("Ghostty compiles %s %s but Renovate selected %s; wait for or select a compatible Ghostty commit", component, compiled, *selected)
	}

	return nil
}

func refreshZig(ctx context.Context, lock *Lock) error {
	data, err := download(ctx, "https://ziglang.org/download/index.json")
	if err != nil {
		return fmt.Errorf("download Zig index: %w", err)
	}

	var index map[string]json.RawMessage
	if err := json.Unmarshal(data, &index); err != nil {
		return fmt.Errorf("decode Zig download index: %w", err)
	}

	rawRelease, ok := index[lock.Zig.Version]
	if !ok {
		return fmt.Errorf("Zig download index has no release %s", lock.Zig.Version)
	}

	var release zigRelease
	if err := json.Unmarshal(rawRelease, &release); err != nil {
		return fmt.Errorf("decode Zig %s download metadata: %w", lock.Zig.Version, err)
	}

	source := release.Source

	linux := release.LinuxX8664
	if source.Tarball == "" || linux.Tarball == "" {
		return fmt.Errorf("Zig %s download index is missing source or x86_64-linux artifacts", lock.Zig.Version)
	}

	if !sha256Pattern.MatchString(source.Shasum) || !sha256Pattern.MatchString(linux.Shasum) {
		return fmt.Errorf("Zig %s download index has an invalid source or x86_64-linux SHA-256", lock.Zig.Version)
	}

	for name, artifact := range map[string]zigIndexEntry{
		"source":       source,
		"x86_64-linux": linux,
	} {
		archive, downloadErr := download(ctx, artifact.Tarball)
		if downloadErr != nil {
			return fmt.Errorf("download Zig %s %s archive: %w", lock.Zig.Version, name, downloadErr)
		}

		if actual := bytesSHA256(archive); actual != artifact.Shasum {
			return fmt.Errorf("Zig %s %s archive SHA-256 = %s, want index digest %s", lock.Zig.Version, name, actual, artifact.Shasum)
		}
	}

	lock.Zig.SourceURL = source.Tarball
	lock.Zig.SourceSHA256 = source.Shasum
	lock.Zig.LinuxX8664URL = linux.Tarball
	lock.Zig.LinuxX8664SHA256 = linux.Shasum

	license, err := download(ctx, "https://raw.githubusercontent.com/ziglang/zig/"+lock.Zig.Version+"/LICENSE")
	if err != nil {
		return fmt.Errorf("download Zig license: %w", err)
	}

	lock.Zig.LicenseSHA256 = bytesSHA256(license)

	return nil
}

func refreshSPDXTools(ctx context.Context, lock *Lock) error {
	tag := "v" + lock.SPDXTools.Version
	assetName := fmt.Sprintf("tools-java-%s.zip", lock.SPDXTools.Version)

	archive, assetURL, _, err := verifiedGitHubReleaseAsset(ctx, "spdx/tools-java", tag, assetName)
	if err != nil {
		return fmt.Errorf("download SPDX validation tools: %w", err)
	}

	lock.SPDXTools.URL = assetURL
	lock.SPDXTools.SHA256 = bytesSHA256(archive)

	return nil
}

func refreshAppleArtifact(ctx context.Context, lock *Lock) error {
	tag := "libghostty-vt-" + lock.Ghostty.Commit[:7]

	archive, assetURL, releaseBody, err := verifiedGitHubReleaseAsset(ctx, "d0ugal/graith", tag, "libghostty-vt.xcframework.zip")
	if err != nil {
		return fmt.Errorf("download Apple artifact for Ghostty %s (publish the reviewed artifact before regenerating): %w", lock.Ghostty.Commit, err)
	}

	digest := bytesSHA256(archive)
	if !strings.Contains(releaseBody, lock.Ghostty.Commit) || !strings.Contains(releaseBody, "SPM checksum: "+digest) {
		return fmt.Errorf("apple artifact release %s is not bound to full Ghostty commit %s and checksum %s", tag, lock.Ghostty.Commit, digest)
	}

	lock.Ghostty.AppleArtifact.URL = assetURL
	lock.Ghostty.AppleArtifact.SHA256 = digest

	return nil
}

func synchronizeHeaders(source, root string, lock *Lock) error {
	from := filepath.Join(source, "include/ghostty")

	to := filepath.Join(root, "gui/shared/Sources/CGhosttyVT/include/ghostty")
	if err := os.RemoveAll(to); err != nil {
		return fmt.Errorf("remove stale committed Ghostty headers: %w", err)
	}

	if err := copyTree(from, to); err != nil {
		return err
	}

	digest, err := TreeSHA256(to)
	if err != nil {
		return err
	}

	lock.Ghostty.HeadersSHA256 = digest

	return nil
}

func writeGeneratedMetadata(root string, lock Lock) error {
	spdx, err := RenderSPDX(lock)
	if err != nil {
		return err
	}
	// Generated repository metadata is intentionally world-readable.
	if err := os.WriteFile(filepath.Join(root, SPDXFilename), spdx, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("write native SPDX inventory: %w", err)
	}

	noticesPath := filepath.Join(root, NoticesFilename)

	notices, err := os.ReadFile(noticesPath)
	if err != nil {
		return fmt.Errorf("read native notices: %w", err)
	}

	updated, err := ReplaceNoticesInventory(string(notices), lock)
	if err != nil {
		return err
	}

	if err := os.WriteFile(noticesPath, []byte(updated), 0o644); err != nil { //nolint:gosec // committed notice is public
		return fmt.Errorf("write native notices: %w", err)
	}

	return nil
}

func updateSwiftPackage(root string, lock Lock) error {
	path := filepath.Join(root, "gui/shared/Package.swift")

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Swift package: %w", err)
	}

	updated := swiftArtifactChecksumPattern.ReplaceAllString(
		swiftArtifactURLPattern.ReplaceAllString(string(data), `url: "`+lock.Ghostty.AppleArtifact.URL+`"`),
		`checksum: "`+lock.Ghostty.AppleArtifact.SHA256+`"`,
	)
	if updated == string(data) && (!strings.Contains(updated, lock.Ghostty.AppleArtifact.URL) || !strings.Contains(updated, lock.Ghostty.AppleArtifact.SHA256)) {
		return errors.New("swift package Apple artifact fields were not found")
	}

	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil { //nolint:gosec // committed manifest is public
		return fmt.Errorf("write Swift package: %w", err)
	}

	return nil
}

func checkoutCommit(ctx context.Context, repository, commit, destination string) error {
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return err
	}

	if _, err := run(ctx, "", "git", "-C", destination, "init", "-q"); err != nil {
		return err
	}

	if _, err := run(ctx, "", "git", "-C", destination, "remote", "add", "origin", repository); err != nil {
		return err
	}

	if _, err := run(ctx, "", "git", "-C", destination, "fetch", "--depth", "1", "origin", commit); err != nil {
		return err
	}

	if _, err := run(ctx, "", "git", "-C", destination, "checkout", "-q", "--detach", "FETCH_HEAD"); err != nil {
		return err
	}

	return nil
}

func resolveGitHubTag(ctx context.Context, repository, tag string) (string, error) {
	remote := "https://github.com/" + repository + ".git"

	output, err := run(ctx, "", "git", "ls-remote", remote, "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	if err != nil {
		return "", err
	}

	var direct string

	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}

		if strings.HasSuffix(fields[1], "^{}") {
			return fields[0], nil
		}

		direct = fields[0]
	}

	if !fullSHAPattern.MatchString(direct) {
		return "", fmt.Errorf("tag %s was not found in %s", tag, repository)
	}

	return direct, nil
}

func requireDependencyURL(rawURL, prefix string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}

	if parsed.Scheme != "https" || parsed.Host != "deps.files.ghostty.org" || !strings.HasPrefix(filepath.Base(parsed.Path), prefix) {
		return fmt.Errorf("untrusted Ghostty dependency URL %q", rawURL)
	}

	return nil
}

func verifiedGitHubReleaseAsset(ctx context.Context, repository, tag, assetName string) ([]byte, string, string, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repository, url.PathEscape(tag))

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, "", "", err
	}

	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "graith-libghostty-dependency-generator")

	if token := firstNonempty(os.Getenv("GITHUB_TOKEN"), os.Getenv("RENOVATE_GITHUB_COM_TOKEN")); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	metadata, err := downloadRequest(request)
	if err != nil {
		return nil, "", "", fmt.Errorf("read GitHub release %s/%s: %w", repository, tag, err)
	}

	var release githubRelease
	if err := json.Unmarshal(metadata, &release); err != nil {
		return nil, "", "", fmt.Errorf("decode GitHub release %s/%s: %w", repository, tag, err)
	}

	var assetURL, expectedDigest string

	for _, asset := range release.Assets {
		if asset.Name != assetName {
			continue
		}

		if assetURL != "" {
			return nil, "", "", fmt.Errorf("GitHub release %s/%s has duplicate asset %s", repository, tag, assetName)
		}

		assetURL = asset.BrowserDownloadURL
		expectedDigest = strings.TrimPrefix(asset.Digest, "sha256:")
	}

	if assetURL == "" || !sha256Pattern.MatchString(expectedDigest) {
		return nil, "", "", fmt.Errorf("GitHub release %s/%s has no SHA-256-bound asset %s", repository, tag, assetName)
	}

	wantURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repository, tag, assetName)
	if assetURL != wantURL {
		return nil, "", "", fmt.Errorf("GitHub release asset URL = %s, want %s", assetURL, wantURL)
	}

	archive, err := download(ctx, assetURL)
	if err != nil {
		return nil, "", "", err
	}

	if actual := bytesSHA256(archive); actual != expectedDigest {
		return nil, "", "", fmt.Errorf("GitHub release asset %s SHA-256 = %s, want API digest %s", assetName, actual, expectedDigest)
	}

	return archive, assetURL, release.Body, nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func download(ctx context.Context, rawURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}

	return downloadRequest(request)
}

func downloadRequest(request *http.Request) ([]byte, error) {
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", request.URL, response.Status)
	}

	reader := io.LimitReader(response.Body, maxDependencyDownload+1)

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	if len(data) > maxDependencyDownload {
		return nil, fmt.Errorf("GET %s exceeded %d bytes", request.URL, maxDependencyDownload)
	}

	return data, nil
}

func tarGzipMember(archive []byte, suffix string) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open gzip archive: %w", err)
	}
	defer func() {
		_ = gzipReader.Close()
	}()

	var matched []byte

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("read tar archive: %w", err)
		}

		cleanName := path.Clean(header.Name)
		if cleanName == "." || cleanName == ".." || strings.HasPrefix(cleanName, "../") || path.IsAbs(cleanName) {
			return nil, fmt.Errorf("archive contains unsafe path %q", header.Name)
		}

		if header.Typeflag != tar.TypeReg || !strings.HasSuffix(cleanName, suffix) {
			continue
		}

		if matched != nil {
			return nil, fmt.Errorf("archive contains more than one member ending in %s", suffix)
		}

		data, err := io.ReadAll(io.LimitReader(tarReader, (4<<20)+1))
		if err != nil {
			return nil, err
		}

		if len(data) > 4<<20 {
			return nil, fmt.Errorf("archive member %s exceeded 4 MiB", cleanName)
		}

		matched = data
	}

	if matched != nil {
		return matched, nil
	}

	return nil, fmt.Errorf("archive does not contain %s", suffix)
}

func capture(data []byte, expression string) (string, error) {
	match := regexp.MustCompile(expression).FindSubmatch(data)
	if len(match) != 2 {
		return "", fmt.Errorf("pattern %q did not match exactly one value", expression)
	}

	return string(match[1]), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}

	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()

	if copyErr != nil {
		return "", fmt.Errorf("hash %s: %w", path, copyErr)
	}

	if closeErr != nil {
		return "", fmt.Errorf("close %s: %w", path, closeErr)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func bytesSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}

		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}

		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported header tree member %s", path)
		}

		data, err := os.ReadFile(path) //nolint:gosec // source is an exact, private checkout tree
		if err != nil {
			return err
		}

		if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // committed headers are public
			return err
		}

		return nil
	})
}

// withGeneratedUnitRollback keeps local and Renovate runs transaction-like:
// any late generation or verification error restores every managed file and
// the complete committed header tree. A successful run leaves the reviewed
// diff visible for one commit; an interrupted process is the only case this
// in-process rollback cannot cover.
func withGeneratedUnitRollback(root string, mutate func() error) (returnErr error) {
	backup, err := os.MkdirTemp("", "graith-libghostty-deps-backup-")
	if err != nil {
		return fmt.Errorf("create native dependency backup: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(backup)
	}()

	for _, name := range generatedUnitFiles {
		if err := copyFile(filepath.Join(root, name), filepath.Join(backup, name)); err != nil {
			return fmt.Errorf("back up %s: %w", name, err)
		}
	}

	if err := copyTree(filepath.Join(root, generatedHeaders), filepath.Join(backup, generatedHeaders)); err != nil {
		return fmt.Errorf("back up committed Ghostty headers: %w", err)
	}

	mutationErr := mutate()
	if mutationErr == nil {
		return nil
	}

	returnErr = mutationErr

	var restoreProblems []error

	for _, name := range generatedUnitFiles {
		if err := copyFile(filepath.Join(backup, name), filepath.Join(root, name)); err != nil {
			restoreProblems = append(restoreProblems, fmt.Errorf("restore %s: %w", name, err))
		}
	}

	headerRoot := filepath.Join(root, generatedHeaders)
	if err := os.RemoveAll(headerRoot); err != nil {
		restoreProblems = append(restoreProblems, fmt.Errorf("remove changed Ghostty headers: %w", err))
	} else if err := copyTree(filepath.Join(backup, generatedHeaders), headerRoot); err != nil {
		restoreProblems = append(restoreProblems, fmt.Errorf("restore committed Ghostty headers: %w", err))
	}

	if restoreErr := errors.Join(restoreProblems...); restoreErr != nil {
		return errors.Join(returnErr, fmt.Errorf("restore native dependency unit after failure: %w", restoreErr))
	}

	return returnErr
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}

	return os.WriteFile(destination, data, 0o644) //nolint:gosec // generated dependency metadata is public
}

func run(ctx context.Context, directory, name string, args ...string) ([]byte, error) {
	// Commands and arguments are passed directly without a shell; all callers
	// select the executable from fixed generator code.
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec
	command.Dir = directory

	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run %s %s: %w\n%s", name, strings.Join(args, " "), err, output)
	}

	return output, nil
}
