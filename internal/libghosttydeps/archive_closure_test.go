package libghosttydeps

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var baseArchiveMembers = []string{
	"abort.o", "base64.o", "codepoint_width.o", "compiler_rt.o", "index_of.o",
	"libghostty-vt-static_zcu.o", "libhighway_zcu.o", "per_target.o",
	"simdutf.o", "targets.o", "vt.o",
}

// scriptFunction returns one top-level shell function from the native helper
// so the audited closure is tested through the real script text.
func scriptFunction(t *testing.T, name string) string {
	t.Helper()

	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	script, err := os.ReadFile(filepath.Join(sourceRoot, "scripts", "libghostty-native.sh"))
	if err != nil {
		t.Fatal(err)
	}

	text := string(script)

	begin := strings.Index(text, "\n"+name+"() {")
	if begin < 0 {
		t.Fatalf("scripts/libghostty-native.sh has no %s", name)
	}

	end := strings.Index(text[begin+1:], "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of %s", name)
	}

	return text[begin+1 : begin+1+end+3]
}

// scriptLockLoaders returns the WUFFS_* assignments the script derives from the
// lock, so lock-driven behaviour is exercised end to end.
func scriptLockLoaders(t *testing.T) string {
	t.Helper()

	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	script, err := os.ReadFile(filepath.Join(sourceRoot, "scripts", "libghostty-native.sh"))
	if err != nil {
		t.Fatal(err)
	}

	loaders := regexp.MustCompile(`(?m)^WUFFS_[A-Z0-9_]+=.*$`).FindAllString(string(script), -1)
	if len(loaders) == 0 {
		t.Fatal("script does not load Wuffs values from the lock")
	}

	return strings.Join(loaders, "\n")
}

func expectedMembersFor(t *testing.T, lockJSON []byte) (string, error) {
	t.Helper()

	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required by the native helper")
	}

	lockPath := filepath.Join(t.TempDir(), LockFilename)
	if err := os.WriteFile(lockPath, lockJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"set -euo pipefail",
		`die() { echo "error: $*" >&2; return 1; }`,
		`DEPENDENCY_LOCK="$LOCK"`,
		scriptLockLoaders(t),
		scriptFunction(t, "expected_static_archive_members"),
		"expected_static_archive_members",
	}, "\n")

	cmd := exec.Command("bash", "-c", program)

	cmd.Env = append(os.Environ(), "LOCK="+lockPath)

	output, err := cmd.Output()

	return strings.TrimSuffix(string(output), "\n"), err
}

func lockJSONWithWuffs(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()

	lock := committedLock(t)
	lock.Wuffs = testWuffsEntry()

	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}

	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	mutate(document)

	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

// lockJSONWithoutWuffs is the committed lock with Wuffs removed, so the
// 11-member closure stays covered once the committed lock tracks Wuffs.
func lockJSONWithoutWuffs(t *testing.T) []byte {
	t.Helper()

	lock := committedLock(t)
	lock.Wuffs = nil

	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func sortedMembers(extra ...string) string {
	members := append(append([]string{}, baseArchiveMembers...), extra...)

	// LC_ALL=C order is plain byte order, matching sort.Strings.
	sort.Strings(members)

	return strings.Join(members, "\n")
}

func TestExpectedArchiveMembersAreLockDriven(t *testing.T) {
	committed := lockJSONWithoutWuffs(t)

	tests := []struct {
		name    string
		lock    []byte
		want    string
		wantErr string
	}{
		{"lock without Wuffs keeps the 11-member closure", committed, sortedMembers(), ""},
		{"lock tracking Wuffs adds exactly its object", lockJSONWithWuffs(t, func(map[string]any) {}), sortedMembers("wuffs-v0.4.o"), ""},
		{"member name follows the lock's C source", lockJSONWithWuffs(t, func(document map[string]any) {
			document["wuffs"].(map[string]any)["cSource"] = "release/c/wuffs-v0.5.c"
		}), sortedMembers("wuffs-v0.5.o"), ""},
		{"traversing C source fails closed", lockJSONWithWuffs(t, func(document map[string]any) {
			document["wuffs"].(map[string]any)["cSource"] = "release/c/../../evil.c"
		}), "", "unsupported Wuffs C source path"},
		{"wildcard C source fails closed", lockJSONWithWuffs(t, func(document map[string]any) {
			document["wuffs"].(map[string]any)["cSource"] = "release/c/wuffs-v*.c"
		}), "", "unsupported Wuffs C source path"},
		{"object name instead of source fails closed", lockJSONWithWuffs(t, func(document map[string]any) {
			document["wuffs"].(map[string]any)["cSource"] = "wuffs-v0.4.o"
		}), "", "unsupported Wuffs C source path"},
		{"tracked Wuffs without a C source fails closed", lockJSONWithWuffs(t, func(document map[string]any) {
			delete(document["wuffs"].(map[string]any), "cSource")
		}), "", "without naming its C source"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := expectedMembersFor(t, test.lock)
			if test.wantErr != "" {
				if err == nil {
					t.Fatalf("expected failure, got members %q", got)
				}

				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || !strings.Contains(string(exitErr.Stderr), test.wantErr) {
					t.Fatalf("error = %v, want stderr containing %q", err, test.wantErr)
				}

				if got != "" {
					t.Fatalf("a failed expectation still printed members: %q", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("expected_static_archive_members: %v", err)
			}

			if got != test.want {
				t.Fatalf("members:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

func TestAuditRequiresExactSetEquality(t *testing.T) {
	withWuffs := lockJSONWithWuffs(t, func(map[string]any) {})

	committed := lockJSONWithoutWuffs(t)

	tests := []struct {
		name        string
		lock        []byte
		actual      string
		wantMatch   bool
		wantExtra   string
		wantMissing string
	}{
		{"lock without Wuffs matches the 11 members", committed, sortedMembers(), true, "", ""},
		{"lock with Wuffs matches the 12 members", withWuffs, sortedMembers("wuffs-v0.4.o"), true, "", ""},
		// The failure observed on the Ghostty update that first linked Wuffs.
		{"lock without Wuffs rejects the Wuffs object", committed, sortedMembers("wuffs-v0.4.o"), false, "wuffs-v0.4.o", ""},
		{"lock with Wuffs rejects a missing Wuffs object", withWuffs, sortedMembers(), false, "", "wuffs-v0.4.o"},
		{"lock with Wuffs rejects another Wuffs series", withWuffs, sortedMembers("wuffs-v0.5.o"), false, "wuffs-v0.5.o", "wuffs-v0.4.o"},
		{"lock with Wuffs rejects any unrelated extra", withWuffs, sortedMembers("wuffs-v0.4.o", "braw.o"), false, "braw.o", ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			expected, err := expectedMembersFor(t, test.lock)
			if err != nil {
				t.Fatal(err)
			}

			matches := test.actual == expected
			if matches != test.wantMatch {
				t.Fatalf("exact equality = %v, want %v\nexpected:\n%s\nactual:\n%s", matches, test.wantMatch, expected, test.actual)
			}

			if test.wantMatch {
				return
			}

			helper := scriptFunction(t, "report_archive_member_diff")
			cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+helper+"\nreport_archive_member_diff \"$EXPECTED\" \"$ACTUAL\"")

			cmd.Env = append(os.Environ(), "EXPECTED="+expected, "ACTUAL="+test.actual)

			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("diff helper failed: %v\n%s", err, output)
			}

			if test.wantExtra != "" && !strings.Contains(string(output), "  + "+test.wantExtra+"\n") {
				t.Errorf("diff does not name extra %s:\n%s", test.wantExtra, output)
			}

			if test.wantMissing != "" && !strings.Contains(string(output), "  - "+test.wantMissing+"\n") {
				t.Errorf("diff does not name missing %s:\n%s", test.wantMissing, output)
			}
		})
	}
}

func TestArchiveClosureMessageReportsLockDrivenCount(t *testing.T) {
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	script, err := os.ReadFile(filepath.Join(sourceRoot, "scripts", "libghostty-native.sh"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(script), "11-member") {
		t.Fatal("the helper hardcodes the 11-member closure; the count must follow the lock")
	}
}
