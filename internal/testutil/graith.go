package testutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const isolatedGraithProfile = "test"

type savedEnvironment struct {
	name    string
	value   string
	present bool
}

type environmentOverride struct {
	name  string
	value string
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
	root, err := os.MkdirTemp("", "graith-test-")
	if err != nil {
		return nil, fmt.Errorf("create temporary root: %w", err)
	}

	overrides := []environmentOverride{
		{name: "XDG_CONFIG_HOME", value: filepath.Join(root, "config")},
		{name: "XDG_DATA_HOME", value: filepath.Join(root, "data")},
		{name: "XDG_RUNTIME_DIR", value: filepath.Join(root, "runtime")},
		{name: "GRAITH_PROFILE", value: isolatedGraithProfile},
		{name: "GRAITH_TOKEN", value: ""},
	}

	for _, entry := range overrides[:3] {
		if err := os.MkdirAll(entry.value, 0o700); err != nil {
			_ = os.RemoveAll(root)

			return nil, fmt.Errorf("create isolated %s: %w", entry.name, err)
		}
	}

	saved := make([]savedEnvironment, len(overrides))
	for index, override := range overrides {
		saved[index].name = override.name
		saved[index].value, saved[index].present = os.LookupEnv(override.name)

		if err := os.Setenv(override.name, override.value); err != nil {
			cleanupErr := restoreGraithEnvironment(saved[:index], root)

			return nil, errors.Join(fmt.Errorf("set %s: %w", override.name, err), cleanupErr)
		}
	}

	return func() error {
		return restoreGraithEnvironment(saved, root)
	}, nil
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
