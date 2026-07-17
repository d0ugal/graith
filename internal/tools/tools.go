// Package tools resolves the external executables graith shells out to: git,
// gh, the notification shell, osascript, ps, and lsof. Historically these names
// and paths were hard-coded ("git", "gh", "sh", "osascript", "/bin/ps",
// "/usr/sbin/lsof"), which broke Nix/custom-PATH installs, wrapper binaries, and
// alternate shells (issue #1238).
//
// A single process-wide resolver holds the effective set. The daemon and the
// stateless CLI configure it once at startup from the [tools] config block;
// every git/store/notification/resource call site then reads it. Reads are
// lock-free (an atomic snapshot) so the many concurrent daemon goroutines can
// resolve a tool without contention, and a startup Configure races with nothing
// because it runs before serving begins.
//
// This is deliberately NOT a generic arbitrary-argv escape hatch: only the
// executable is configurable. Semantic subcommands (git rev-parse, gh api) and
// sandbox protocol flags stay in code.
package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
)

// Config names each external executable graith may run. A field may be a bare
// command name resolved on PATH ("git", "hub") or an absolute/relative path to
// a specific binary ("/run/current-system/sw/bin/git"). An empty field inherits
// the built-in default from Defaults.
type Config struct {
	Git       string
	GH        string
	Shell     string
	OSAScript string
	PS        string
	Lsof      string
}

// Defaults returns the built-in executable set, preserving the names and paths
// graith used before they became configurable.
func Defaults() Config {
	return Config{
		Git:       "git",
		GH:        "gh",
		Shell:     "sh",
		OSAScript: "osascript",
		PS:        "/bin/ps",
		Lsof:      "/usr/sbin/lsof",
	}
}

// merge fills empty fields of c from Defaults so the resolver never returns an
// empty executable name.
func merge(c Config) Config {
	d := Defaults()

	if c.Git == "" {
		c.Git = d.Git
	}

	if c.GH == "" {
		c.GH = d.GH
	}

	if c.Shell == "" {
		c.Shell = d.Shell
	}

	if c.OSAScript == "" {
		c.OSAScript = d.OSAScript
	}

	if c.PS == "" {
		c.PS = d.PS
	}

	if c.Lsof == "" {
		c.Lsof = d.Lsof
	}

	return c
}

// current holds the effective, fully-merged Config once Configure runs. Until
// then it is nil and get() falls back to the built-in defaults, so the resolver
// is usable (with defaults) even in a process that never calls Configure.
var current atomic.Pointer[Config]

// Configure installs the effective executable set, filling any empty field with
// its built-in default. It is intended to run once at startup before the daemon
// serves requests (or before the CLI runs a command). Calling it with the zero
// Config restores the built-in defaults.
func Configure(c Config) {
	m := merge(c)
	current.Store(&m)
}

// Reset restores the built-in defaults. It exists for tests that mutate the
// resolver and need to undo the change.
func Reset() {
	Configure(Config{})
}

func get() Config {
	if c := current.Load(); c != nil {
		return *c
	}

	return Defaults()
}

// Snapshot returns the current fully-merged executable set in a single atomic
// load. A multi-step operation that needs several tools (e.g. create/fork uses
// both git and gh for username discovery) captures one Snapshot so every
// subprocess runs against the same generation even if a config reload swaps the
// registry mid-operation (#1287).
func Snapshot() Config {
	return get()
}

// Git returns the configured git executable.
func Git() string { return get().Git }

// GH returns the configured GitHub CLI executable.
func GH() string { return get().GH }

// Shell returns the configured shell used to run notification and trigger
// commands (invoked as `<shell> -c <command>`).
func Shell() string { return get().Shell }

// OSAScript returns the configured osascript executable (macOS notifications).
func OSAScript() string { return get().OSAScript }

// PS returns the configured process-listing executable.
func PS() string { return get().PS }

// Lsof returns the configured open-files listing executable.
func Lsof() string { return get().Lsof }

// Validate checks that every explicitly-set field of c is resolvable, so a
// misconfigured tool fails loudly at startup rather than at the first git fetch
// or notification. Empty fields are skipped: a default bare name (e.g.
// "osascript" on Linux) must retain plain PATH-lookup semantics and is not an
// error until actually used.
//
// A value containing a path separator (absolute or relative) must point at an
// existing, regular, executable file. A bare name must be found on PATH.
func Validate(c Config) error {
	var errs []error

	for _, f := range []struct{ key, val string }{
		{"git", c.Git},
		{"gh", c.GH},
		{"shell", c.Shell},
		{"osascript", c.OSAScript},
		{"ps", c.PS},
		{"lsof", c.Lsof},
	} {
		if f.val == "" {
			continue
		}

		if err := validateExecutable(f.val); err != nil {
			errs = append(errs, fmt.Errorf("tools.%s %q: %w", f.key, f.val, err))
		}
	}

	return errors.Join(errs...)
}

// validateExecutable reports whether name is usable as an external command. A
// value that names a path (contains os.PathSeparator) is checked directly: it
// must exist, be a regular file, and carry an executable bit. A bare name is
// resolved through PATH with exec.LookPath.
func validateExecutable(name string) error {
	if !hasPathSeparator(name) {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("not found on PATH: %w", err)
		}

		return nil
	}

	info, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("cannot stat: %w", err)
	}

	if info.IsDir() {
		return errors.New("is a directory, not an executable")
	}

	if info.Mode()&0o111 == 0 {
		return errors.New("is not executable (no execute permission)")
	}

	return nil
}

func hasPathSeparator(name string) bool {
	for i := 0; i < len(name); i++ {
		if name[i] == os.PathSeparator || name[i] == '/' {
			return true
		}
	}

	return false
}
