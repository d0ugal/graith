package config

import (
	"strings"
	"testing"
)

func TestDaemonServiceConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		vars    []string
		wantErr string
	}{
		{name: "empty"},
		{name: "explicit sockets and credentials", vars: []string{"SSH_AUTH_SOCK", "ANTHROPIC_API_KEY"}},
		{name: "invalid name", vars: []string{"CANNY-NAME"}, wantErr: "invalid environment name"},
		{name: "duplicate", vars: []string{"BRAW", "BRAW"}, wantErr: "duplicate variable"},
		{name: "identity", vars: []string{"HOME"}, wantErr: "reserved variable"},
		{name: "profile", vars: []string{"GRAITH_PROFILE"}, wantErr: "reserved variable"},
		{name: "session", vars: []string{"GRAITH_SESSION_ID"}, wantErr: "reserved variable"},
		{name: "loader", vars: []string{"DYLD_INSERT_LIBRARIES"}, wantErr: "reserved variable"},
		{name: "linux loader", vars: []string{"LD_PRELOAD"}, wantErr: "reserved variable"},
		{name: "launch service", vars: []string{"XPC_SERVICE_NAME"}, wantErr: "reserved variable"},
		{name: "core foundation", vars: []string{"__CFBundleIdentifier"}, wantErr: "reserved variable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := (DaemonServiceConfig{InheritEnv: tt.vars}).Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDaemonServiceDefaults(t *testing.T) {
	t.Parallel()

	cfg := Default()
	if len(cfg.DaemonService.InheritEnv) != 0 {
		t.Fatalf("default daemon service inherit_env = %v, want empty", cfg.DaemonService.InheritEnv)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
}
