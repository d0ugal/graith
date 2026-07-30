package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchConfigEmbeddedDefaults(t *testing.T) {
	s := Default().Search

	raw := map[string]struct {
		got  int
		want int
	}{
		"default_limit":         {s.DefaultLimit, SearchDefaultLimitDefault},
		"max_limit":             {s.MaxLimit, SearchMaxLimitDefault},
		"max_window":            {s.MaxWindow, SearchMaxWindowDefault},
		"snippet_runes":         {s.SnippetRunes, SearchSnippetRunesDefault},
		"snippet_context":       {s.SnippetContext, SearchSnippetContextDefault},
		"max_turn_runes":        {s.MaxTurnRunes, SearchMaxTurnRunesDefault},
		"max_source_bytes":      {s.MaxSourceBytes, SearchMaxSourceBytesDefault},
		"max_source_turns":      {s.MaxSourceTurns, SearchMaxSourceTurnsDefault},
		"max_cache_bytes":       {s.MaxCacheBytes, SearchMaxCacheBytesDefault},
		"max_cache_entry_bytes": {s.MaxCacheEntryBytes, SearchMaxCacheEntryBytesDefault},
	}

	for name, test := range raw {
		t.Run(name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("Default().Search.%s = %d, want %d (missing from default_config.toml?)", name, test.got, test.want)
			}
		})
	}

	limits := s.Limits()
	resolved := map[string]struct {
		got  int
		want int
	}{
		"default_limit":         {limits.DefaultLimit, SearchDefaultLimitDefault},
		"max_limit":             {limits.MaxLimit, SearchMaxLimitDefault},
		"max_window":            {limits.MaxWindow, SearchMaxWindowDefault},
		"snippet_runes":         {limits.SnippetRunes, SearchSnippetRunesDefault},
		"snippet_context":       {limits.SnippetContext, SearchSnippetContextDefault},
		"max_turn_runes":        {limits.MaxTurnRunes, SearchMaxTurnRunesDefault},
		"max_source_bytes":      {limits.MaxSourceBytes, SearchMaxSourceBytesDefault},
		"max_source_turns":      {limits.MaxSourceTurns, SearchMaxSourceTurnsDefault},
		"max_cache_bytes":       {limits.MaxCacheBytes, SearchMaxCacheBytesDefault},
		"max_cache_entry_bytes": {limits.MaxCacheEntryBytes, SearchMaxCacheEntryBytesDefault},
	}

	for name, test := range resolved {
		t.Run("resolved_"+name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("Search.Limits().%s = %d, want %d", name, test.got, test.want)
			}
		})
	}
}

func TestSearchConfigLimits(t *testing.T) {
	t.Run("empty falls back to defaults", func(t *testing.T) {
		limits := SearchConfig{}.Limits()

		if limits.DefaultLimit != SearchDefaultLimitDefault {
			t.Errorf("default limit = %d, want %d", limits.DefaultLimit, SearchDefaultLimitDefault)
		}

		if limits.MaxSourceBytes != SearchMaxSourceBytesDefault {
			t.Errorf("max source bytes = %d, want %d", limits.MaxSourceBytes, SearchMaxSourceBytesDefault)
		}

		if limits.MaxCacheEntryBytes != SearchMaxCacheEntryBytesDefault {
			t.Errorf("max cache entry bytes = %d, want %d", limits.MaxCacheEntryBytes, SearchMaxCacheEntryBytesDefault)
		}
	})

	t.Run("explicit values honoured", func(t *testing.T) {
		limits := SearchConfig{
			DefaultLimit:       7,
			MaxLimit:           11,
			MaxWindow:          13,
			SnippetRunes:       17,
			SnippetContext:     5,
			MaxTurnRunes:       19,
			MaxSourceBytes:     23,
			MaxSourceTurns:     29,
			MaxCacheBytes:      31,
			MaxCacheEntryBytes: 23,
		}.Limits()

		got := map[string]int{
			"default_limit":         limits.DefaultLimit,
			"max_limit":             limits.MaxLimit,
			"max_window":            limits.MaxWindow,
			"snippet_runes":         limits.SnippetRunes,
			"snippet_context":       limits.SnippetContext,
			"max_turn_runes":        limits.MaxTurnRunes,
			"max_source_bytes":      limits.MaxSourceBytes,
			"max_source_turns":      limits.MaxSourceTurns,
			"max_cache_bytes":       limits.MaxCacheBytes,
			"max_cache_entry_bytes": limits.MaxCacheEntryBytes,
		}
		want := map[string]int{
			"default_limit":         7,
			"max_limit":             11,
			"max_window":            13,
			"snippet_runes":         17,
			"snippet_context":       5,
			"max_turn_runes":        19,
			"max_source_bytes":      23,
			"max_source_turns":      29,
			"max_cache_bytes":       31,
			"max_cache_entry_bytes": 23,
		}

		for name, want := range want {
			if got[name] != want {
				t.Errorf("%s = %d, want %d", name, got[name], want)
			}
		}
	})

	t.Run("relationships clamp partial configs", func(t *testing.T) {
		limits := SearchConfig{
			DefaultLimit:       50,
			MaxLimit:           40,
			MaxWindow:          10,
			SnippetRunes:       20,
			SnippetContext:     30,
			MaxCacheBytes:      100,
			MaxCacheEntryBytes: 200,
		}.Limits()

		if limits.MaxLimit != 10 {
			t.Errorf("max_limit = %d, want clamped to max_window 10", limits.MaxLimit)
		}

		if limits.DefaultLimit != 10 {
			t.Errorf("default_limit = %d, want clamped to max_limit 10", limits.DefaultLimit)
		}

		if limits.SnippetContext != 20 {
			t.Errorf("snippet_context = %d, want clamped to snippet_runes 20", limits.SnippetContext)
		}

		if limits.MaxCacheEntryBytes != 100 {
			t.Errorf("max_cache_entry_bytes = %d, want clamped to max_cache_bytes 100", limits.MaxCacheEntryBytes)
		}
	})
}

func TestValidateSearchLimits(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*Config)
		wantErr string
	}{
		"defaults pass": {
			mutate: func(*Config) {},
		},
		"max limit above ceiling": {
			mutate:  func(c *Config) { c.Search.MaxLimit = SearchMaxLimitCeiling + 1 },
			wantErr: "search.max_limit",
		},
		"max window above ceiling": {
			mutate:  func(c *Config) { c.Search.MaxWindow = SearchMaxWindowCeiling + 1 },
			wantErr: "search.max_window",
		},
		"source bytes above ceiling": {
			mutate:  func(c *Config) { c.Search.MaxSourceBytes = SearchMaxSourceBytesCeiling + 1 },
			wantErr: "search.max_source_bytes",
		},
		"cache entry bytes above ceiling": {
			mutate:  func(c *Config) { c.Search.MaxCacheEntryBytes = SearchMaxCacheEntryBytesCeiling + 1 },
			wantErr: "search.max_cache_entry_bytes",
		},
		"relationship clamp passes": {
			mutate: func(c *Config) {
				c.Search.DefaultLimit = 50
				c.Search.MaxLimit = 40
				c.Search.MaxWindow = 10
				c.Search.SnippetContext = 50
				c.Search.SnippetRunes = 20
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			test.mutate(cfg)

			err := cfg.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", test.wantErr)
			}

			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadPreservesSearchLimitOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[search]
max_window = 25
snippet_runes = 40
max_source_turns = 7
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	limits := cfg.Search.Limits()
	if limits.MaxWindow != 25 {
		t.Errorf("max_window = %d, want 25 (override)", limits.MaxWindow)
	}

	if limits.MaxLimit != 25 {
		t.Errorf("max_limit = %d, want clamped to max_window 25", limits.MaxLimit)
	}

	if limits.SnippetRunes != 40 {
		t.Errorf("snippet_runes = %d, want 40 (override)", limits.SnippetRunes)
	}

	if limits.MaxSourceTurns != 7 {
		t.Errorf("max_source_turns = %d, want 7 (override)", limits.MaxSourceTurns)
	}

	if limits.MaxSourceBytes != SearchMaxSourceBytesDefault {
		t.Errorf("max_source_bytes = %d, want %d (default preserved)", limits.MaxSourceBytes, SearchMaxSourceBytesDefault)
	}
}
