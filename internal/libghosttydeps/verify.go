package libghosttydeps

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Verify(root string) error {
	lock, err := LoadLock(filepath.Join(root, LockFilename))
	if err != nil {
		return err
	}

	var problems []error

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
		for label, want := range map[string]string{
			"Apple artifact URL":      `url: "` + lock.Ghostty.AppleArtifact.URL + `"`,
			"Apple artifact checksum": `checksum: "` + lock.Ghostty.AppleArtifact.SHA256 + `"`,
		} {
			if !bytes.Contains(data, []byte(want)) {
				return fmt.Errorf("%s does not match the lock", label)
			}
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
		for _, field := range []string{".zig.version", ".zig.linuxX8664URL", ".zig.linuxX8664SHA256"} {
			if !strings.Contains(content, field) {
				return fmt.Errorf("native workflow does not read lock field %s", field)
			}
		}

		return nil
	})

	return errors.Join(problems...)
}
