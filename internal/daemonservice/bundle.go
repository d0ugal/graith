package daemonservice

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

var (
	// ManagedBuild is set to true only for a release/development channel that
	// also packages a matching signed Graith.app. Source and go-install builds
	// deliberately retain direct spawning.
	ManagedBuild = "false"
	// ExpectedTeamID and ExpectedRequirementBase64 are stamped into stable builds.
	// Development-signed builds may leave them empty only when DevelopmentBuild
	// is explicitly true. The requirement is base64-encoded so codesign's
	// whitespace and quoting cannot be reinterpreted by go build's -ldflags.
	ExpectedTeamID            = ""
	ExpectedRequirementBase64 = ""
	DevelopmentBuild          = "false"
)

var safeGenerationPart = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

var lookupOSUserByID = user.LookupId

type SignatureInfo struct {
	Identifier  string
	TeamID      string
	Requirement string
}

type BundleExpectations struct {
	Version             string
	Commit              string
	StandalonePath      string
	TeamID              string
	Requirement         string
	AllowDevelopmentSig bool
	VerifySignature     func(string) (SignatureInfo, error)
	VerifyDistribution  func(string) error
}

type ValidatedBundle struct {
	AppPath    string
	Generation Generation
}

func IsManagedBuild() bool { return ManagedBuild == "true" }

func stableSigningExpectation() (string, string, error) {
	if ExpectedTeamID == "" && ExpectedRequirementBase64 == "" {
		return "", "", nil
	}

	if ExpectedTeamID == "" || ExpectedRequirementBase64 == "" {
		return "", "", errors.New("managed build has an incomplete stable signing expectation")
	}

	requirement, err := base64.StdEncoding.DecodeString(ExpectedRequirementBase64)
	if err != nil || len(requirement) == 0 {
		return "", "", errors.New("managed build has an invalid encoded signing requirement")
	}

	return ExpectedTeamID, string(requirement), nil
}

func GenerationID(version, commit string) (string, error) {
	version = strings.Trim(safeGenerationPart.ReplaceAllString(version, "-"), "-")

	commit = strings.Trim(safeGenerationPart.ReplaceAllString(commit, "-"), "-")
	if version == "" || commit == "" || version == "dev" || commit == "unknown" {
		return "", errors.New("managed daemon service requires a concrete version and commit")
	}

	return version + "-" + commit, nil
}

// BundleCandidates returns only package-relative locations. It never searches
// /Applications or trusts an environment override because a matching app must
// come from the same installed release as the CLI.
func BundleCandidates(executable string) []string {
	seen := make(map[string]bool)

	var candidates []string

	for _, path := range []string{executable, evalSymlinksOrSelf(executable)} {
		dir := filepath.Dir(path)
		if dir == "" || seen[dir] {
			continue
		}

		seen[dir] = true
		if filepath.Base(dir) == "MacOS" && filepath.Base(filepath.Dir(filepath.Dir(dir))) == AppBundleName {
			candidates = append(candidates, filepath.Dir(filepath.Dir(dir)))
		}

		candidates = append(candidates,
			filepath.Join(dir, AppBundleName),
			filepath.Join(dir, "..", "libexec", "graith", AppBundleName),
		)
	}

	return uniqueCleanPaths(candidates)
}

func uniqueCleanPaths(paths []string) []string {
	seen := make(map[string]bool)

	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}

	return result
}

func evalSymlinksOrSelf(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	return path
}

// DiscoverBundle distinguishes an absent package payload (explicit unbundled
// fallback) from a present-but-invalid app, which callers must fail closed.
func DiscoverBundle(executable string, expectations BundleExpectations) (ValidatedBundle, bool, error) {
	for _, candidate := range BundleCandidates(executable) {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return ValidatedBundle{}, true, err
		}

		bundle, err := ValidateBundle(candidate, expectations)

		return bundle, true, err
	}

	return ValidatedBundle{}, false, nil
}

func ValidateBundle(appPath string, expectations BundleExpectations) (ValidatedBundle, error) {
	if expectations.VerifySignature == nil {
		return ValidatedBundle{}, errors.New("daemon service signature verifier is required")
	}

	appPath = filepath.Clean(appPath)
	if filepath.Base(appPath) != AppBundleName {
		return ValidatedBundle{}, fmt.Errorf("unexpected daemon service app name %q", filepath.Base(appPath))
	}

	if err := validateSecureAncestors(appPath, os.Geteuid()); err != nil {
		return ValidatedBundle{}, fmt.Errorf("validate daemon service app path: %w", err)
	}

	info, err := os.Lstat(appPath)
	if err != nil {
		return ValidatedBundle{}, err
	}

	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ValidatedBundle{}, errors.New("daemon service app must be a real directory")
	}

	plist, err := readPlistStrings(filepath.Join(appPath, "Contents", "Info.plist"))
	if err != nil {
		return ValidatedBundle{}, fmt.Errorf("read daemon service Info.plist: %w", err)
	}

	manifest := ServiceManifest()
	if plist["CFBundleIdentifier"] != manifest.BundleIdentifier {
		return ValidatedBundle{}, fmt.Errorf("daemon service bundle identifier %q does not match %q", plist["CFBundleIdentifier"], manifest.BundleIdentifier)
	}

	if plist["CFBundleShortVersionString"] != expectations.Version || plist["GraithCommitSHA"] != expectations.Commit {
		return ValidatedBundle{}, fmt.Errorf("daemon service bundle version/commit %s/%s does not match CLI %s/%s", plist["CFBundleShortVersionString"], plist["GraithCommitSHA"], expectations.Version, expectations.Commit)
	}

	if strings.TrimSpace(plist["CFBundleVersion"]) == "" {
		return ValidatedBundle{}, errors.New("daemon service bundle has no build version")
	}

	payload := filepath.Join(appPath, "Contents", "MacOS", DaemonExecutable)

	payloadHash, err := hashRegularFile(payload)
	if err != nil {
		return ValidatedBundle{}, fmt.Errorf("hash daemon service payload: %w", err)
	}

	if plist["GraithPayloadSHA256"] != payloadHash {
		return ValidatedBundle{}, errors.New("daemon service payload hash does not match signed metadata")
	}

	if expectations.StandalonePath != "" {
		standaloneHash, err := hashRegularFile(expectations.StandalonePath)
		if err != nil {
			return ValidatedBundle{}, fmt.Errorf("hash standalone gr: %w", err)
		}

		if standaloneHash != payloadHash {
			return ValidatedBundle{}, errors.New("standalone and embedded gr payloads differ")
		}
	}

	signature, err := expectations.VerifySignature(appPath)
	if err != nil {
		return ValidatedBundle{}, fmt.Errorf("verify daemon service signature: %w", err)
	}

	if signature.Identifier != manifest.BundleIdentifier {
		return ValidatedBundle{}, fmt.Errorf("signed identifier %q does not match %q", signature.Identifier, manifest.BundleIdentifier)
	}

	if expectations.TeamID == "" || expectations.Requirement == "" {
		if !expectations.AllowDevelopmentSig {
			return ValidatedBundle{}, errors.New("stable daemon service build has no expected team or designated requirement")
		}
	} else if signature.TeamID != expectations.TeamID || signature.Requirement != expectations.Requirement {
		return ValidatedBundle{}, errors.New("daemon service signing team or designated requirement mismatch")
	}

	generationID, err := GenerationID(expectations.Version, expectations.Commit)
	if err != nil {
		return ValidatedBundle{}, err
	}

	return ValidatedBundle{
		AppPath: appPath,
		Generation: Generation{
			ID: generationID, AppPath: appPath, Version: expectations.Version,
			BundleBuild: plist["CFBundleVersion"], Commit: expectations.Commit, PayloadHash: payloadHash,
			TeamID: signature.TeamID, Requirement: signature.Requirement,
		},
	}, nil
}

func validateSecureAncestors(path string, uid int) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)

		info, err := os.Lstat(current)
		if err != nil {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %s is a symlink", current)
		}

		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("path component %s is group/other writable", current)
		}

		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (int(stat.Uid) != uid && stat.Uid != 0) {
			return fmt.Errorf("path component %s has untrusted owner", current)
		}
	}

	return nil
}

func hashRegularFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}

	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("payload is not a regular non-symlink file")
	}

	file, err := os.Open(path)
	if err != nil {
		return "", err
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readPlistStrings(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	decoder := xml.NewDecoder(io.LimitReader(file, 1<<20))
	values := make(map[string]string)

	var key string

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		switch start.Name.Local {
		case "key":
			if err := decoder.DecodeElement(&key, &start); err != nil {
				return nil, err
			}
		case "string":
			if key == "" {
				continue
			}

			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return nil, err
			}

			values[key] = value
			key = ""
		}
	}

	return values, nil
}

func OSUserHome(uid int) (string, error) {
	if uid <= 0 || uid != os.Geteuid() {
		return "", errors.New("daemon service UID must match the non-root effective user")
	}

	account, err := lookupOSUserByID(strconv.Itoa(uid))
	if err != nil {
		return "", err
	}

	parsed, err := strconv.Atoi(account.Uid)

	home := filepath.Clean(account.HomeDir)

	if err != nil || parsed != uid || !filepath.IsAbs(account.HomeDir) || home != account.HomeDir || home == string(filepath.Separator) || strings.ContainsAny(home, "\x00\r\n") {
		return "", errors.New("OS user database returned invalid daemon service home")
	}

	info, err := os.Lstat(home)
	if err != nil {
		return "", fmt.Errorf("inspect OS user home: %w", err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("OS user home ownership, mode, or type is invalid")
	}

	return home, nil
}

func CacheRoot(uid int) (string, error) {
	home, err := OSUserHome(uid)
	if err != nil {
		return "", err
	}

	return filepath.Join(home, "Library", "Application Support", "Graith", "services"), nil
}

func ReceiptRoot(uid int) (string, error) {
	root, err := CacheRoot(uid)
	if err != nil {
		return "", err
	}

	return filepath.Join(root, "control"), nil
}

func CacheBundle(source ValidatedBundle, expectations BundleExpectations, uid int) (ValidatedBundle, error) {
	return CacheBundleContext(context.Background(), source, expectations, uid)
}

func CacheBundleContext(ctx context.Context, source ValidatedBundle, expectations BundleExpectations, uid int) (ValidatedBundle, error) {
	root, err := CacheRoot(uid)
	if err != nil {
		return ValidatedBundle{}, err
	}

	if err := ensureCacheRoot(root, uid); err != nil {
		return ValidatedBundle{}, err
	}

	return cacheBundleAtRootContext(ctx, source, expectations, root)
}

func cacheBundleAtRoot(source ValidatedBundle, expectations BundleExpectations, root string) (ValidatedBundle, error) {
	return cacheBundleAtRootContext(context.Background(), source, expectations, root)
}

func cacheBundleAtRootContext(ctx context.Context, source ValidatedBundle, expectations BundleExpectations, root string) (ValidatedBundle, error) {
	if err := ctx.Err(); err != nil {
		return ValidatedBundle{}, err
	}

	destinationDir := filepath.Join(root, source.Generation.ID)

	destinationApp := filepath.Join(destinationDir, AppBundleName)
	if _, err := os.Lstat(destinationDir); err == nil {
		cached, validateErr := ValidateBundle(destinationApp, expectations)
		if validateErr != nil {
			return ValidatedBundle{}, fmt.Errorf("validate existing daemon service cache: %w", validateErr)
		}

		return cached, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ValidatedBundle{}, err
	}

	tempDir, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return ValidatedBundle{}, err
	}

	if err := os.Chmod(tempDir, 0o700); err != nil { // #nosec G302 -- owner-only cache staging is required.
		_ = os.RemoveAll(tempDir)
		return ValidatedBundle{}, err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	tempApp := filepath.Join(tempDir, AppBundleName)
	if err := copySignedBundle(ctx, source.AppPath, tempApp); err != nil {
		return ValidatedBundle{}, fmt.Errorf("copy daemon service bundle: %w", err)
	}

	if _, err := ValidateBundle(tempApp, expectations); err != nil {
		return ValidatedBundle{}, fmt.Errorf("validate copied daemon service bundle: %w", err)
	}

	if expectations.VerifyDistribution != nil {
		if err := expectations.VerifyDistribution(tempApp); err != nil {
			return ValidatedBundle{}, fmt.Errorf("validate copied daemon service distribution: %w", err)
		}
	}

	if err := os.Rename(tempDir, destinationDir); err != nil {
		if _, statErr := os.Stat(destinationDir); statErr == nil {
			cached, validateErr := ValidateBundle(destinationApp, expectations)
			if validateErr != nil {
				return ValidatedBundle{}, errors.Join(err, validateErr)
			}

			return cached, nil
		}

		return ValidatedBundle{}, err
	}

	if err := syncDirectory(root); err != nil {
		return ValidatedBundle{}, err
	}

	cached, err := ValidateBundle(destinationApp, expectations)
	if err != nil {
		return ValidatedBundle{}, err
	}

	return cached, nil
}

func ensureCacheRoot(root string, uid int) error {
	home, err := OSUserHome(uid)
	if err != nil {
		return err
	}

	if !strings.HasPrefix(root, home+string(filepath.Separator)) {
		return errors.New("daemon service cache is outside OS user home")
	}

	if err := validateSecureAncestors(home, uid); err != nil {
		return err
	}

	current := home

	components := []string{"Library", "Application Support", "Graith", "services"}
	for index, component := range components {
		current = filepath.Join(current, component)

		mode := os.FileMode(0o755)
		if index >= 2 {
			mode = 0o700
		}

		if err := os.Mkdir(current, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}

		info, err := os.Lstat(current)
		if err != nil {
			return err
		}

		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("unsafe daemon service cache component %s", current)
		}

		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != uid {
			return fmt.Errorf("daemon service cache component %s is not user-owned", current)
		}

		if index >= 2 {
			if err := os.Chmod(current, 0o700); err != nil { // #nosec G302 -- control/cache roots are deliberately owner-only.
				return err
			}
		}
	}

	return nil
}

func validatedCachedBundles(root string, uid int, expectations BundleExpectations) ([]ValidatedBundle, error) {
	if err := validateSecureAncestors(root, uid); err != nil {
		return nil, err
	}

	if err := secureOwnedPath(root, uid, true); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var bundles []ValidatedBundle

	for _, entry := range entries {
		// CacheBundle stages copies in dot-prefixed directories before an atomic
		// rename. A hard-killed installer can leave one behind, and Finder can add
		// .DS_Store. Neither is a published generation, so ignore private entries
		// while retaining strict validation for every visible generation name.
		if entry.Name() == "control" || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		generationDir := filepath.Join(root, entry.Name())

		info, err := os.Lstat(generationDir)
		if err != nil {
			return nil, err
		}

		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("unsafe daemon service cache entry %s", generationDir)
		}

		app := filepath.Join(generationDir, AppBundleName)

		plist, err := readPlistStrings(filepath.Join(app, "Contents", "Info.plist"))
		if err != nil {
			return nil, fmt.Errorf("inspect cached daemon service generation %s: %w", entry.Name(), err)
		}

		candidateExpectations := expectations
		candidateExpectations.Version = plist["CFBundleShortVersionString"]
		candidateExpectations.Commit = plist["GraithCommitSHA"]
		candidateExpectations.StandalonePath = ""

		bundle, err := ValidateBundle(app, candidateExpectations)
		if err != nil {
			return nil, fmt.Errorf("validate cached daemon service generation %s: %w", entry.Name(), err)
		}

		if entry.Name() != bundle.Generation.ID {
			return nil, fmt.Errorf("cached daemon service directory %s does not match generation %s", entry.Name(), bundle.Generation.ID)
		}

		bundles = append(bundles, bundle)
	}

	sort.Slice(bundles, func(i, j int) bool { return bundles[i].Generation.ID < bundles[j].Generation.ID })

	return bundles, nil
}
