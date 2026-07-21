package libghosttydeps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	LockFilename    = "libghostty-native.lock.json"
	SPDXFilename    = "libghostty-native.spdx.json"
	NoticesFilename = "THIRD_PARTY_NOTICES.libghostty.md"
)

var (
	fullSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Lock struct {
	SchemaVersion int          `json:"schemaVersion"`
	GoLibghostty  GoDependency `json:"goLibghostty"`
	Ghostty       Ghostty      `json:"ghostty"`
	Zig           Zig          `json:"zig"`
	Uucode        Uucode       `json:"uucode"`
	Highway       Highway      `json:"highway"`
	Simdutf       Simdutf      `json:"simdutf"`
	SPDXTools     SPDXTools    `json:"spdxTools"`
}

type GoDependency struct {
	Repository          string `json:"repository"`
	RenovateRef         string `json:"renovateRef"`
	Commit              string `json:"commit"`
	Version             string `json:"version"`
	ModuleSum           string `json:"moduleSum"`
	TestedGhosttyCommit string `json:"testedGhosttyCommit"`
	LicenseSHA256       string `json:"licenseSHA256"`
	LicenseConclusion   string `json:"licenseConclusion"`
}

type Ghostty struct {
	Repository        string        `json:"repository"`
	RenovateRef       string        `json:"renovateRef"`
	Commit            string        `json:"commit"`
	Version           string        `json:"version"`
	HeadersSHA256     string        `json:"headersSHA256"`
	LicenseSHA256     string        `json:"licenseSHA256"`
	LicenseConclusion string        `json:"licenseConclusion"`
	AppleArtifact     AppleArtifact `json:"appleArtifact"`
}

type AppleArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type Zig struct {
	Repository        string `json:"repository"`
	Version           string `json:"version"`
	SourceURL         string `json:"sourceURL"`
	SourceSHA256      string `json:"sourceSHA256"`
	LinuxX8664URL     string `json:"linuxX8664URL"`
	LinuxX8664SHA256  string `json:"linuxX8664SHA256"`
	LicenseSHA256     string `json:"licenseSHA256"`
	LicenseConclusion string `json:"licenseConclusion"`
}

type Uucode struct {
	Repository          string `json:"repository"`
	Version             string `json:"version"`
	SourceURL           string `json:"sourceURL"`
	ZigHash             string `json:"zigHash"`
	ArchiveSHA256       string `json:"archiveSHA256"`
	LicenseSHA256       string `json:"licenseSHA256"`
	DecoderNoticeSHA256 string `json:"decoderNoticeSHA256"`
	UnicodeNoticeSHA256 string `json:"unicodeNoticeSHA256"`
	LicenseConclusion   string `json:"licenseConclusion"`
}

type Highway struct {
	Repository        string `json:"repository"`
	Version           string `json:"version"`
	Commit            string `json:"commit"`
	SourceURL         string `json:"sourceURL"`
	ZigHash           string `json:"zigHash"`
	ArchiveSHA256     string `json:"archiveSHA256"`
	LicenseSHA256     string `json:"licenseSHA256"`
	LicenseConclusion string `json:"licenseConclusion"`
	LicenseDeclared   string `json:"licenseDeclared"`
}

type Simdutf struct {
	Repository        string `json:"repository"`
	Version           string `json:"version"`
	ManifestVersion   string `json:"manifestVersion"`
	Commit            string `json:"commit"`
	CppSHA256         string `json:"cppSHA256"`
	HeaderSHA256      string `json:"headerSHA256"`
	LicenseSHA256     string `json:"licenseSHA256"`
	LicenseConclusion string `json:"licenseConclusion"`
	LicenseDeclared   string `json:"licenseDeclared"`
}

type SPDXTools struct {
	Repository string `json:"repository"`
	Version    string `json:"version"`
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
}

func LoadLock(path string) (Lock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Lock{}, fmt.Errorf("read native dependency lock: %w", err)
	}
	return DecodeLock(data)
}

func DecodeLock(data []byte) (Lock, error) {
	var lock Lock
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lock); err != nil {
		return Lock{}, fmt.Errorf("decode native dependency lock: %w", err)
	}
	if err := lock.Validate(); err != nil {
		return Lock{}, err
	}
	return lock, nil
}

func WriteLock(path string, lock Lock) error {
	if err := lock.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("encode native dependency lock: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write native dependency lock: %w", err)
	}
	return nil
}

func (lock Lock) Validate() error {
	if lock.SchemaVersion != 1 {
		return fmt.Errorf("native dependency lock schemaVersion = %d, want 1", lock.SchemaVersion)
	}
	var problems []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			problems = append(problems, fmt.Errorf(format, args...))
		}
	}
	check(fullSHAPattern.MatchString(lock.GoLibghostty.Commit), "invalid go-libghostty commit")
	check(fullSHAPattern.MatchString(lock.GoLibghostty.TestedGhosttyCommit), "invalid go-libghostty tested Ghostty commit")
	check(fullSHAPattern.MatchString(lock.Ghostty.Commit), "invalid Ghostty commit")
	check(fullSHAPattern.MatchString(lock.Highway.Commit), "invalid Highway commit")
	check(fullSHAPattern.MatchString(lock.Simdutf.Commit), "invalid simdutf commit")
	for name, value := range map[string]string{
		"go-libghostty license": lock.GoLibghostty.LicenseSHA256,
		"Ghostty headers":       lock.Ghostty.HeadersSHA256,
		"Ghostty license":       lock.Ghostty.LicenseSHA256,
		"Apple artifact":        lock.Ghostty.AppleArtifact.SHA256,
		"Zig source":            lock.Zig.SourceSHA256,
		"Zig Linux archive":     lock.Zig.LinuxX8664SHA256,
		"Zig license":           lock.Zig.LicenseSHA256,
		"uucode archive":        lock.Uucode.ArchiveSHA256,
		"uucode license":        lock.Uucode.LicenseSHA256,
		"uucode decoder notice": lock.Uucode.DecoderNoticeSHA256,
		"uucode Unicode notice": lock.Uucode.UnicodeNoticeSHA256,
		"Highway archive":       lock.Highway.ArchiveSHA256,
		"Highway license":       lock.Highway.LicenseSHA256,
		"simdutf source":        lock.Simdutf.CppSHA256,
		"simdutf header":        lock.Simdutf.HeaderSHA256,
		"simdutf license":       lock.Simdutf.LicenseSHA256,
		"SPDX tools archive":    lock.SPDXTools.SHA256,
	} {
		check(sha256Pattern.MatchString(value), "invalid %s SHA-256", name)
	}
	for name, value := range map[string]string{
		"go-libghostty repository": lock.GoLibghostty.Repository,
		"go-libghostty version":    lock.GoLibghostty.Version,
		"go-libghostty sum":        lock.GoLibghostty.ModuleSum,
		"Ghostty repository":       lock.Ghostty.Repository,
		"Ghostty version":          lock.Ghostty.Version,
		"Apple artifact URL":       lock.Ghostty.AppleArtifact.URL,
		"Zig version":              lock.Zig.Version,
		"uucode version":           lock.Uucode.Version,
		"Highway version":          lock.Highway.Version,
		"simdutf version":          lock.Simdutf.Version,
		"SPDX tools version":       lock.SPDXTools.Version,
	} {
		check(value != "", "missing %s", name)
	}
	if len(lock.Ghostty.Commit) >= 7 {
		check(strings.Contains(lock.Ghostty.AppleArtifact.URL, lock.Ghostty.Commit[:7]),
			"Apple artifact URL does not contain the Ghostty short commit")
	}
	check(strings.Contains(lock.SPDXTools.URL, lock.SPDXTools.Version),
		"SPDX tools URL does not contain its version")
	return errors.Join(problems...)
}

// TreeSHA256 binds a generated header tree to both relative paths and bytes.
// Paths are sorted and separated with NUL bytes to make the digest independent
// of filesystem enumeration and impossible to confuse by concatenation.
func TreeSHA256(root string) (string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("enumerate tree %s: %w", root, err)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		if _, err := io.WriteString(hash, relative); err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte{0})
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return "", fmt.Errorf("open tree member %s: %w", relative, err)
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", fmt.Errorf("hash tree member %s: %w", relative, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close tree member %s: %w", relative, closeErr)
		}
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
