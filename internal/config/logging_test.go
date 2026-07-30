package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoggingDefaults(t *testing.T) {
	d := Default()

	if d.Logging.DaemonMaxBytes != DaemonLogMaxBytesDefault {
		t.Errorf("embedded logging.daemon_max_bytes = %d, want %d", d.Logging.DaemonMaxBytes, DaemonLogMaxBytesDefault)
	}

	if d.Logging.DaemonMaxBackups != DaemonLogMaxBackupsDefault {
		t.Errorf("embedded logging.daemon_max_backups = %d, want %d", d.Logging.DaemonMaxBackups, DaemonLogMaxBackupsDefault)
	}

	if got := d.Logging.DaemonMaxBytesOrDefault(); got != DaemonLogMaxBytesDefault {
		t.Errorf("DaemonMaxBytesOrDefault() = %d, want %d", got, DaemonLogMaxBytesDefault)
	}

	if got := d.Logging.DaemonMaxBackupsOrDefault(); got != DaemonLogMaxBackupsDefault {
		t.Errorf("DaemonMaxBackupsOrDefault() = %d, want %d", got, DaemonLogMaxBackupsDefault)
	}
}

func TestLoggingAccessors(t *testing.T) {
	tests := map[string]struct {
		cfg         LoggingConfig
		wantBytes   int64
		wantBackups int
	}{
		"zero disables": {
			cfg:         LoggingConfig{DaemonMaxBytes: 0, DaemonMaxBackups: 0},
			wantBytes:   0,
			wantBackups: 0,
		},
		"negative falls back": {
			cfg:         LoggingConfig{DaemonMaxBytes: -1, DaemonMaxBackups: -1},
			wantBytes:   DaemonLogMaxBytesDefault,
			wantBackups: DaemonLogMaxBackupsDefault,
		},
		"explicit values honoured": {
			cfg:         LoggingConfig{DaemonMaxBytes: 4096, DaemonMaxBackups: 7},
			wantBytes:   4096,
			wantBackups: 7,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := test.cfg.DaemonMaxBytesOrDefault(); got != test.wantBytes {
				t.Errorf("DaemonMaxBytesOrDefault() = %d, want %d", got, test.wantBytes)
			}

			if got := test.cfg.DaemonMaxBackupsOrDefault(); got != test.wantBackups {
				t.Errorf("DaemonMaxBackupsOrDefault() = %d, want %d", got, test.wantBackups)
			}
		})
	}
}

func TestLoggingLoadOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte(`
[logging]
daemon_max_bytes = 0
daemon_max_backups = 1
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Logging.DaemonMaxBytesOrDefault(); got != 0 {
		t.Errorf("daemon_max_bytes = %d, want 0 (unlimited, overridden)", got)
	}

	if got := cfg.Logging.DaemonMaxBackupsOrDefault(); got != 1 {
		t.Errorf("daemon_max_backups = %d, want 1 (overridden)", got)
	}
}
