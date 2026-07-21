package libghosttydeps

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var (
	swiftArtifactURLPattern      = regexp.MustCompile(`url: "(https://github\.com/d0ugal/graith/releases/download/libghostty-vt-[^"]+/libghostty-vt\.xcframework\.zip)"`)
	swiftArtifactChecksumPattern = regexp.MustCompile(`checksum: "([0-9a-f]{64})"`)
)

func Verify(root string) error {
	return verify(root, true)
}

// VerifyGenerated checks every mechanical projection without accepting new
// license evidence. Generation uses this mode so it can produce a reviewable,
// deliberately red PR when a license or required notice changed; the normal
// Verify path remains the merge gate and requires explicit review bindings.
func VerifyGenerated(root string) error {
	return verify(root, false)
}

func verify(root string, requireLicenseReviews bool) error {
	lock, err := LoadLock(filepath.Join(root, LockFilename))
	if err != nil {
		return err
	}

	var problems []error

	if requireLicenseReviews {
		if reviewErr := VerifyLicenseReviews(lock); reviewErr != nil {
			problems = append(problems, reviewErr)
		}
	}

	checkFile := func(name string, check func([]byte) error) {
		data, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			problems = append(problems, fmt.Errorf("read %s: %w", name, readErr))
			return
		}

		if checkErr := check(data); checkErr != nil {
			problems = append(problems, fmt.Errorf("%s: %w", name, checkErr))
		}
	}

	checkFile(SPDXFilename, func(actual []byte) error {
		expected, renderErr := RenderSPDX(lock)
		if renderErr != nil {
			return renderErr
		}

		if !bytes.Equal(actual, expected) {
			return errors.New("generated SPDX inventory is stale; run scripts/libghostty-native.sh generate-dependency-unit")
		}

		return nil
	})
	checkFile(NoticesFilename, func(actual []byte) error {
		expected, replaceErr := ReplaceNoticesInventory(string(actual), lock)
		if replaceErr != nil {
			return replaceErr
		}

		if string(actual) != expected {
			return errors.New("generated notice inventory is stale; run scripts/libghostty-native.sh generate-dependency-unit")
		}

		return nil
	})
	checkFile("go.mod", func(data []byte) error {
		want := "\tgo.mitchellh.com/libghostty " + lock.GoLibghostty.Version + "\n"
		if !bytes.Contains(data, []byte(want)) {
			return fmt.Errorf("go-libghostty requirement does not match %s", lock.GoLibghostty.Version)
		}

		return nil
	})
	checkFile("go.sum", func(data []byte) error {
		want := "go.mitchellh.com/libghostty " + lock.GoLibghostty.Version + " " + lock.GoLibghostty.ModuleSum + "\n"
		if !bytes.Contains(data, []byte(want)) {
			return errors.New("go-libghostty module sum does not match the lock")
		}

		return nil
	})
	checkFile("gui/shared/Package.swift", func(data []byte) error {
		urlMatches := swiftArtifactURLPattern.FindAllSubmatch(data, -1)
		checksumMatches := swiftArtifactChecksumPattern.FindAllSubmatch(data, -1)

		if len(urlMatches) != 1 || string(urlMatches[0][1]) != lock.Ghostty.AppleArtifact.URL {
			return errors.New("apple artifact URL does not uniquely match the lock")
		}

		if len(checksumMatches) != 1 || string(checksumMatches[0][1]) != lock.Ghostty.AppleArtifact.SHA256 {
			return errors.New("apple artifact checksum does not uniquely match the lock")
		}

		return nil
	})

	headerRoot := filepath.Join(root, "gui/shared/Sources/CGhosttyVT/include/ghostty")

	actualHeaders, hashErr := TreeSHA256(headerRoot)
	if hashErr != nil {
		problems = append(problems, hashErr)
	} else if actualHeaders != lock.Ghostty.HeadersSHA256 {
		problems = append(problems, fmt.Errorf("committed Ghostty header tree SHA-256 = %s, want %s", actualHeaders, lock.Ghostty.HeadersSHA256))
	}

	checkFile("scripts/libghostty-native.sh", func(data []byte) error {
		if !bytes.Contains(data, []byte(`DEPENDENCY_LOCK="$REPO_DIR/libghostty-native.lock.json"`)) {
			return errors.New("native validation script does not load the canonical lock")
		}

		return nil
	})
	checkFile("gui/shared/build-libghostty.sh", func(data []byte) error {
		if !bytes.Contains(data, []byte(`DEPENDENCY_LOCK="$REPO_ROOT/libghostty-native.lock.json"`)) {
			return errors.New("apple build script does not load the canonical lock")
		}

		return nil
	})
	checkFile(".github/workflows/libghostty-native.yml", func(data []byte) error {
		content := string(data)
		for description, expression := range map[string]string{
			"Zig version":         `zig_version="\$\(jq -er '\.zig\.version' libghostty-native\.lock\.json\)"`,
			"Zig archive URL":     `zig_url="\$\(jq -er '\.zig\.linuxX8664URL' libghostty-native\.lock\.json\)"`,
			"Zig archive SHA-256": `zig_sha256="\$\(jq -er '\.zig\.linuxX8664SHA256' libghostty-native\.lock\.json\)"`,
		} {
			if !regexp.MustCompile(expression).MatchString(content) {
				return fmt.Errorf("native workflow does not use the lock-derived %s", description)
			}
		}

		for _, expression := range []string{
			`curl [^\n]*\n[^\n]*"\$zig_url" --output`,
			`printf [^\n]*"\$zig_sha256"`,
			`zig" version\)" = "\$zig_version"`,
		} {
			if !regexp.MustCompile(expression).MatchString(content) {
				return errors.New("native workflow does not consume the lock-derived Zig values")
			}
		}

		return nil
	})

	return errors.Join(problems...)
}
