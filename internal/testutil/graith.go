package testutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	isolatedGraithProfile  = "test"
	portableUnixSocketPath = 104
)

type savedEnvironment struct {
	name    string
	value   string
	present bool
}

type environmentOverride struct {
	name      string
	value     string
	directory bool
	unset     bool
}

// RunWithIsolatedGraith runs a test suite with configuration, data, runtime,
// and credentials isolated from the host Graith environment. The callback
// shape lets TestMain compose this wrapper with other suite-wide isolation;
// cleanup completes before the returned exit code is passed to os.Exit.
func RunWithIsolatedGraith(run func() int) (code int) {
	cleanup, err := isolateGraith()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "isolate Graith test environment: %v\n", err)

		return 1
	}

	defer func() {
		if err := cleanup(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "clean up isolated Graith test environment: %v\n", err)

			if code == 0 {
				code = 1
			}
		}
	}()

	return run()
}

func isolateGraith() (func() error, error) {
	root, err := isolatedGraithRoot()
	if err != nil {
		return nil, fmt.Errorf("create temporary root: %w", err)
	}

	overrides := []environmentOverride{
		{name: "XDG_CONFIG_HOME", value: filepath.Join(root, "config"), directory: true},
		{name: "XDG_DATA_HOME", value: filepath.Join(root, "data"), directory: true},
		{name: "XDG_RUNTIME_DIR", value: filepath.Join(root, "run"), directory: true},
		{name: "GRAITH_PROFILE", value: isolatedGraithProfile},
		{name: "GRAITH_TOKEN", value: ""},
		{name: "GRAITH_SESSION_ID", unset: true},
		{name: "GRAITH_SESSION_NAME", unset: true},
	}

	for _, entry := range overrides {
		if !entry.directory {
			continue
		}

		if err := os.MkdirAll(entry.value, 0o700); err != nil {
			_ = os.RemoveAll(root)

			return nil, fmt.Errorf("create isolated %s: %w", entry.name, err)
		}
	}

	saved := make([]savedEnvironment, len(overrides))
	for index, override := range overrides {
		saved[index].name = override.name
		saved[index].value, saved[index].present = os.LookupEnv(override.name)

		var err error
		if override.unset {
			err = os.Unsetenv(override.name)
		} else {
			err = os.Setenv(override.name, override.value)
		}

		if err != nil {
			cleanupErr := restoreGraithEnvironment(saved[:index], root)

			return nil, errors.Join(fmt.Errorf("override %s: %w", override.name, err), cleanupErr)
		}
	}

	return func() error {
		return restoreGraithEnvironment(saved, root)
	}, nil
}

func isolatedGraithRoot() (string, error) {
	root, err := os.MkdirTemp("", "grt-")
	if err != nil {
		return "", err
	}

	if runtime.GOOS == "windows" || isolatedSocketPathFits(root) {
		return root, nil
	}

	if err := os.RemoveAll(root); err != nil {
		return "", fmt.Errorf("remove overlong temporary root: %w", err)
	}

	// TMPDIR may already be a deep Graith session path. Unix socket addresses
	// are limited to 104 bytes on macOS, so fall back to the short system temp
	// root when the resolved isolated socket would not fit.
	root, err = os.MkdirTemp("/tmp", "grt-")
	if err != nil {
		return "", err
	}

	if !isolatedSocketPathFits(root) {
		_ = os.RemoveAll(root)

		return "", fmt.Errorf("temporary root %s cannot fit a portable Unix socket path", root)
	}

	return root, nil
}

func isolatedSocketPathFits(root string) bool {
	socketPath := filepath.Join(root, "run", "graith-"+isolatedGraithProfile, "graith.sock")

	return len(socketPath) < portableUnixSocketPath
}

func restoreGraithEnvironment(values []savedEnvironment, root string) error {
	var cleanupErr error

	for _, entry := range values {
		var err error
		if entry.present {
			err = os.Setenv(entry.name, entry.value)
		} else {
			err = os.Unsetenv(entry.name)
		}

		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore %s: %w", entry.name, err))
		}
	}

	if err := os.RemoveAll(root); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove temporary root: %w", err))
	}

	return cleanupErr
}
