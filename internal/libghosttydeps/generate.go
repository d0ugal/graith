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

func Generate(ctx context.Context, root string) error {
	lockPath := filepath.Join(root, LockFilename)

	lock, err := LoadLock(lockPath)
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

	if err := Verify(root); err != nil {
		return fmt.Errorf("verify generated native dependency unit: %w", err)
	}

	return nil
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

	simdutfLicense, err := download(ctx, fmt.Sprintf("https://raw.githubusercontent.com/%s/v%s/LICENSE-MIT", lock.Simdutf.Repository, lock.Simdutf.Version))
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

	previous, err := DecodeLock(output)
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
	lock.SPDXTools.URL = fmt.Sprintf("https://github.com/spdx/tools-java/releases/download/v%[1]s/tools-java-%[1]s.zip", lock.SPDXTools.Version)

	archive, err := download(ctx, lock.SPDXTools.URL)
	if err != nil {
		return fmt.Errorf("download SPDX validation tools: %w", err)
	}

	lock.SPDXTools.SHA256 = bytesSHA256(archive)

	return nil
}

func refreshAppleArtifact(ctx context.Context, lock *Lock) error {
	lock.Ghostty.AppleArtifact.URL = fmt.Sprintf("https://github.com/d0ugal/graith/releases/download/libghostty-vt-%s/libghostty-vt.xcframework.zip", lock.Ghostty.Commit[:7])

	archive, err := download(ctx, lock.Ghostty.AppleArtifact.URL)
	if err != nil {
		return fmt.Errorf("download Apple artifact for Ghostty %s (publish the reviewed artifact before regenerating): %w", lock.Ghostty.Commit, err)
	}

	lock.Ghostty.AppleArtifact.SHA256 = bytesSHA256(archive)

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

	urlPattern := regexp.MustCompile(`url: "https://github\.com/d0ugal/graith/releases/download/libghostty-vt-[^"]+/libghostty-vt\.xcframework\.zip"`)
	checksumPattern := regexp.MustCompile(`checksum: "[0-9a-f]{64}"`)

	updated := checksumPattern.ReplaceAllString(
		urlPattern.ReplaceAllString(string(data), `url: "`+lock.Ghostty.AppleArtifact.URL+`"`),
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

func download(ctx context.Context, rawURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", rawURL, response.Status)
	}

	reader := io.LimitReader(response.Body, maxDependencyDownload+1)

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	if len(data) > maxDependencyDownload {
		return nil, fmt.Errorf("GET %s exceeded %d bytes", rawURL, maxDependencyDownload)
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

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("read tar archive: %w", err)
		}

		if header.Typeflag != tar.TypeReg || !strings.HasSuffix(header.Name, suffix) {
			continue
		}

		data, err := io.ReadAll(io.LimitReader(tarReader, 4<<20))
		if err != nil {
			return nil, err
		}

		return data, nil
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
