package scripts_test

// Tests for scripts/rpm-preset-keygrips.sh — the helper the goreleaser
// workflow uses to preload the signing passphrase into gpg-agent before
// rpm --addsign. The regression this guards (issue #767): the passphrase
// must be preset for EVERY keygrip of the signing key, not just the primary
// key's, because rpm may drive gpg to a dedicated signing subkey.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const scriptPath = "rpm-preset-keygrips.sh"

// gpg --with-keygrip --list-secret-keys --with-colons output for a key with a
// primary key AND a dedicated signing subkey — the layout that broke in #767.
// Each `grp:` record carries a distinct keygrip in field 10.
const primaryPlusSubkeyListing = `sec:u:255:22:AABBCCDDEEFF0011:1600000000:::u:::scESC:::+:::23::0:
fpr:::::::::0123456789ABCDEF0123456789ABCDEF01234567:
grp:::::::::PRIMARYKEYGRIP000000000000000000000000:
uid:u::::1600000000::0000000000000000000000000000000000000000::graith release <release@graith.dev>::::::::::0:
ssb:u:255:22:1122334455667788:1600000000::::::s:::+:::23:
fpr:::::::::FEDCBA9876543210FEDCBA9876543210FEDCBA98:
grp:::::::::SUBKEYGRIP1111111111111111111111111111:
`

// A single-key layout (primary has the `s` capability, no signing subkey):
// exactly one keygrip.
const singleKeyListing = `sec:u:255:22:AABBCCDDEEFF0011:1600000000:::u:::scESC:::+:::23::0:
fpr:::::::::0123456789ABCDEF0123456789ABCDEF01234567:
grp:::::::::LONESOMEKEYGRIP00000000000000000000000:
uid:u::::1600000000::0000000000000000000000000000000000000000::graith release <release@graith.dev>::::::::::0:
`

// wantPassphrase is the passphrase the script is invoked with; the fake preset
// asserts it arrives on stdin (not argv).
const wantPassphrase = "hunter2"

// writeFakePreset writes a stand-in gpg-preset-passphrase that records the
// keygrip it was asked to preset (the final argument) to logPath, one per
// line. It also verifies the passphrase is delivered on stdin — the argv must
// NOT contain --passphrase (that would leak the secret to `ps`). It lets the
// test observe exactly which keygrips were preset without a real gpg-agent.
func writeFakePreset(t *testing.T, dir, logPath string) string {
	t.Helper()

	preset := filepath.Join(dir, "fake-preset-passphrase")

	body := "#!/usr/bin/env bash\n" +
		"# Records the final arg (the keygrip) for the test to assert on, and\n" +
		"# checks the passphrase comes on stdin rather than in argv.\n" +
		"set -euo pipefail\n" +
		"for arg; do\n" +
		"  if [ \"$arg\" = \"--passphrase\" ]; then\n" +
		"    echo 'fake-preset: passphrase leaked on argv' >&2; exit 3\n" +
		"  fi\n" +
		"  kg=\"$arg\"\n" +
		"done\n" +
		"read -r pass || true\n" +
		"if [ \"$pass\" != " + shellQuote(wantPassphrase) + " ]; then\n" +
		"  echo \"fake-preset: passphrase on stdin was '$pass'\" >&2; exit 4\n" +
		"fi\n" +
		"printf '%s\\n' \"$kg\" >> " + shellQuote(logPath) + "\n"
	if err := os.WriteFile(preset, []byte(body), 0o755); err != nil { //nolint:gosec // G306: stub must be executable — the helper execs it directly
		t.Fatalf("write fake preset: %v", err)
	}

	return preset
}

// shellQuote single-quotes a path for safe embedding in the generated script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runScript pipes listing into the helper and returns the recorded keygrips
// plus any run error.
func runScript(t *testing.T, listing string) ([]string, error) {
	t.Helper()

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "preset.log")
	preset := writeFakePreset(t, dir, logPath)

	cmd := exec.Command("bash", scriptPath, preset)
	cmd.Stdin = strings.NewReader(listing)

	cmd.Env = append(os.Environ(), "GPG_PASSPHRASE="+wantPassphrase)
	out, err := cmd.CombinedOutput()

	var recorded []string

	if data, rerr := os.ReadFile(logPath); rerr == nil {
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				recorded = append(recorded, line)
			}
		}
	}

	if err != nil {
		t.Logf("script output: %s", out)
	}

	return recorded, err
}

// TestPresetsEveryKeygrip is the core #767 regression: with a primary +
// signing subkey, BOTH keygrips must be preset. Reintroducing the original
// first-only bug (`awk ... {print $10; exit}`) in the script would record just
// one keygrip and fail this test.
func TestPresetsEveryKeygrip(t *testing.T) {
	recorded, err := runScript(t, primaryPlusSubkeyListing)
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}

	want := []string{
		"PRIMARYKEYGRIP000000000000000000000000",
		"SUBKEYGRIP1111111111111111111111111111",
	}
	if len(recorded) != len(want) {
		t.Fatalf("preset %d keygrips, want %d: %v", len(recorded), len(want), recorded)
	}

	for i, w := range want {
		if recorded[i] != w {
			t.Errorf("keygrip[%d] = %q, want %q", i, recorded[i], w)
		}
	}
}

// TestPresetsSingleKey keeps the common single-key case working: one keygrip,
// one preset.
func TestPresetsSingleKey(t *testing.T) {
	recorded, err := runScript(t, singleKeyListing)
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}

	want := []string{"LONESOMEKEYGRIP00000000000000000000000"}
	if len(recorded) != 1 || recorded[0] != want[0] {
		t.Fatalf("preset %v, want %v", recorded, want)
	}
}

// TestFailsWhenNoKeygrips fails closed: an empty/keygrip-less listing must
// error rather than silently preset nothing (which would hang signing later).
func TestFailsWhenNoKeygrips(t *testing.T) {
	recorded, err := runScript(t, "sec:u:255:22:AABBCCDDEEFF0011:1600000000:::u:::scESC:::+:::23::0:\n")
	if err == nil {
		t.Fatal("expected non-zero exit for a listing with no keygrips")
	}

	if len(recorded) != 0 {
		t.Fatalf("expected no keygrips preset, got %v", recorded)
	}
}
