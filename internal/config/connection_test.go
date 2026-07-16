package config

import (
	"strings"
	"testing"
	"time"
)

// connectionDurations resolves every ConnectionConfig accessor into a name-keyed
// map. Centralising the accessor calls here (rather than repeating the column in
// each test's table) keeps the tests' expected-value tables short enough not to
// trip the dupl linter, and gives one place to update if an accessor is added.
func connectionDurations(c ConnectionConfig) map[string]time.Duration {
	return map[string]time.Duration{
		"dial":               c.DialTimeoutDuration(),
		"handshake":          c.HandshakeTimeoutDuration(),
		"start":              c.StartTimeoutDuration(),
		"start_poll":         c.StartPollIntervalDuration(),
		"reconnect":          c.ReconnectTimeoutDuration(),
		"reconnect_interval": c.ReconnectIntervalDuration(),
		"remote_dial":        c.RemoteDialTimeoutDuration(),
		"remote_handshake":   c.RemoteHandshakeTimeoutDuration(),
		"remote_pairing":     c.RemotePairingTimeoutDuration(),
	}
}

// assertConnectionDurations compares every accessor of c against want.
func assertConnectionDurations(t *testing.T, label string, c ConnectionConfig, want map[string]time.Duration) {
	t.Helper()

	got := connectionDurations(c)
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s %s = %v, want %v", label, name, got[name], w)
		}
	}
}

// TestConnectionConfig_EmbeddedDefaults asserts the embedded default config
// reproduces exactly the connection deadlines that were hard-coded before issue
// #1242, so a fresh install dials/handshakes/retries identically. It asserts the
// RAW string fields parsed from the embedded TOML first: an accessor would
// silently fall back to its Go constant if the embedded key were blanked,
// masking the drift (see the "config default fallback defeated by embedded TOML"
// trap). The accessors are then checked to confirm both agree.
func TestConnectionConfig_EmbeddedDefaults(t *testing.T) {
	c := Default().Connection

	rawChecks := []struct {
		name string
		got  string
		want string
	}{
		{"dial_timeout", c.DialTimeout, "500ms"},
		{"handshake_timeout", c.HandshakeTimeout, "5s"},
		{"start_timeout", c.StartTimeout, "5s"},
		{"start_poll_interval", c.StartPollInterval, "50ms"},
		{"reconnect_timeout", c.ReconnectTimeout, "10s"},
		{"reconnect_interval", c.ReconnectInterval, "250ms"},
		{"remote_dial_timeout", c.RemoteDialTimeout, "10s"},
		{"remote_handshake_timeout", c.RemoteHandshakeTimeout, "15s"},
		{"remote_pairing_timeout", c.RemotePairingTimeout, "11m"},
	}
	for _, tc := range rawChecks {
		if tc.got != tc.want {
			t.Errorf("embedded connection %s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	assertConnectionDurations(t, "default connection", c, map[string]time.Duration{
		"dial":               500 * time.Millisecond,
		"handshake":          5 * time.Second,
		"start":              5 * time.Second,
		"start_poll":         50 * time.Millisecond,
		"reconnect":          10 * time.Second,
		"reconnect_interval": 250 * time.Millisecond,
		"remote_dial":        10 * time.Second,
		"remote_handshake":   15 * time.Second,
		"remote_pairing":     11 * time.Minute,
	})

	if err := Default().Validate(); err != nil {
		t.Errorf("default config Validate = %v, want nil", err)
	}
}

// TestConnectionConfig_Accessors covers empty, valid, unparseable, and
// non-positive inputs for every accessor. All connection deadlines and intervals
// treat a zero/negative value as "use the default" (a zero timeout would abort
// immediately; a zero interval would busy-loop).
func TestConnectionConfig_Accessors(t *testing.T) {
	t.Run("empty falls back to defaults", func(t *testing.T) {
		var c ConnectionConfig // all zero-value strings

		assertConnectionDurations(t, "empty", c, map[string]time.Duration{
			"dial":               ConnectionDialTimeoutDefault,
			"handshake":          ConnectionHandshakeTimeoutDefault,
			"start":              ConnectionStartTimeoutDefault,
			"start_poll":         ConnectionStartPollIntervalDefault,
			"reconnect":          ConnectionReconnectTimeoutDefault,
			"reconnect_interval": ConnectionReconnectIntervalDefault,
			"remote_dial":        ConnectionRemoteDialTimeoutDefault,
			"remote_handshake":   ConnectionRemoteHandshakeTimeoutDefault,
			"remote_pairing":     ConnectionRemotePairingTimeoutDefault,
		})
	})

	t.Run("valid values are honoured", func(t *testing.T) {
		c := ConnectionConfig{
			DialTimeout:            "1s",
			HandshakeTimeout:       "20s",
			StartTimeout:           "30s",
			StartPollInterval:      "5ms",
			ReconnectTimeout:       "1m",
			ReconnectInterval:      "500ms",
			RemoteDialTimeout:      "25s",
			RemoteHandshakeTimeout: "40s",
			RemotePairingTimeout:   "20m",
		}

		assertConnectionDurations(t, "valid", c, map[string]time.Duration{
			"dial":               time.Second,
			"handshake":          20 * time.Second,
			"start":              30 * time.Second,
			"start_poll":         5 * time.Millisecond,
			"reconnect":          time.Minute,
			"reconnect_interval": 500 * time.Millisecond,
			"remote_dial":        25 * time.Second,
			"remote_handshake":   40 * time.Second,
			"remote_pairing":     20 * time.Minute,
		})
	})

	t.Run("unparseable and non-positive fall back to defaults", func(t *testing.T) {
		c := ConnectionConfig{
			DialTimeout:       "dreich",
			HandshakeTimeout:  "0s",
			StartPollInterval: "-5ms",
		}

		assertConnectionDurations(t, "fallback", c, map[string]time.Duration{
			"dial":       ConnectionDialTimeoutDefault,
			"handshake":  ConnectionHandshakeTimeoutDefault,
			"start_poll": ConnectionStartPollIntervalDefault,
		})
	})
}

func TestConnectionOverrideViaLoad(t *testing.T) {
	cfg, err := loadTOML(t, `
[connection]
dial_timeout = "2s"
handshake_timeout = "12s"
start_timeout = "42s"
start_poll_interval = "7ms"
reconnect_timeout = "33s"
reconnect_interval = "100ms"
remote_dial_timeout = "44s"
remote_handshake_timeout = "55s"
remote_pairing_timeout = "22m"
`)
	if err != nil {
		t.Fatal(err)
	}

	assertConnectionDurations(t, "loaded", cfg.Connection, map[string]time.Duration{
		"dial":               2 * time.Second,
		"handshake":          12 * time.Second,
		"start":              42 * time.Second,
		"start_poll":         7 * time.Millisecond,
		"reconnect":          33 * time.Second,
		"reconnect_interval": 100 * time.Millisecond,
		"remote_dial":        44 * time.Second,
		"remote_handshake":   55 * time.Second,
		"remote_pairing":     22 * time.Minute,
	})
}

func TestConnectionRejectsUnparseable(t *testing.T) {
	_, err := loadTOML(t, `
[connection]
dial_timeout = "soon"
`)
	if err == nil {
		t.Fatal("Load with bad connection.dial_timeout = nil, want error")
	}

	if !strings.Contains(err.Error(), "connection.dial_timeout") {
		t.Errorf("error %q missing connection.dial_timeout", err)
	}
}

func TestConnectionRejectsNonPositive(t *testing.T) {
	_, err := loadTOML(t, `
[connection]
reconnect_interval = "0s"
`)
	if err == nil {
		t.Fatal("Load with connection.reconnect_interval = 0s returned nil, want error")
	}

	if !strings.Contains(err.Error(), "greater than zero") {
		t.Errorf("error %q missing 'greater than zero'", err)
	}
}
