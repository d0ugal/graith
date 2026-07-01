package daemon

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func TestResolveSummary_Fresh(t *testing.T) {
	now := time.Now()
	s := SessionState{
		Status:       StatusRunning,
		SummaryText:  "Exploring code",
		SummarySetAt: &now,
	}
	cfg := &config.Config{}

	info := toSessionInfo(s, cfg, nil)
	if info.SummaryText != "Exploring code" {
		t.Errorf("SummaryText = %q, want %q", info.SummaryText, "Exploring code")
	}

	if info.SummaryFaded {
		t.Error("should not be faded")
	}
}

func TestResolveSummary_FadedWhenIdleAndOld(t *testing.T) {
	old := time.Now().Add(-10 * time.Minute)
	s := SessionState{
		Status:       StatusRunning,
		SummaryText:  "Done",
		SummarySetAt: &old,
	}
	cfg := &config.Config{}

	info := toSessionInfo(s, cfg, nil)
	if info.SummaryText != "Done" {
		t.Errorf("SummaryText = %q, want %q", info.SummaryText, "Done")
	}

	if !info.SummaryFaded {
		t.Error("should be faded when idle and old")
	}
}

func TestResolveSummary_ExpiredWhenActiveAndOld(t *testing.T) {
	old := time.Now().Add(-10 * time.Minute)
	recentOutput := time.Now().Add(-30 * time.Second)
	s := SessionState{
		Status:       StatusRunning,
		SummaryText:  "Exploring code",
		SummarySetAt: &old,
		LastOutputAt: &recentOutput,
	}
	cfg := &config.Config{}

	info := toSessionInfo(s, cfg, nil)
	if info.SummaryText != "" {
		t.Errorf("SummaryText should be empty when expired, got %q", info.SummaryText)
	}
}

func TestResolveSummary_FallbackToHookToolName(t *testing.T) {
	old := time.Now().Add(-10 * time.Minute)
	recentOutput := time.Now().Add(-30 * time.Second)
	s := SessionState{
		Status:       StatusRunning,
		SummaryText:  "Exploring code",
		SummarySetAt: &old,
		LastOutputAt: &recentOutput,
	}
	cfg := &config.Config{}
	hr := &hookReport{
		ToolName:           "Bash",
		AuthoritativeUntil: time.Now().Add(30 * time.Second),
	}

	info := toSessionInfo(s, cfg, hr)
	if info.SummaryText != "Using Bash" {
		t.Errorf("SummaryText = %q, want %q", info.SummaryText, "Using Bash")
	}

	if info.SummaryFaded {
		t.Error("hook-derived status should not be faded")
	}
}

func TestResolveSummary_StoppedSessionFades(t *testing.T) {
	old := time.Now().Add(-10 * time.Minute)
	s := SessionState{
		Status:       StatusStopped,
		SummaryText:  "Done",
		SummarySetAt: &old,
	}
	cfg := &config.Config{}

	info := toSessionInfo(s, cfg, nil)
	if info.SummaryText != "Done" {
		t.Errorf("SummaryText = %q, want %q", info.SummaryText, "Done")
	}

	if !info.SummaryFaded {
		t.Error("stopped session with old status should be faded")
	}
}

func TestResolveSummary_CustomTTL(t *testing.T) {
	sixMinAgo := time.Now().Add(-6 * time.Minute)
	recentOutput := time.Now().Add(-30 * time.Second)
	s := SessionState{
		Status:       StatusRunning,
		SummaryText:  "Waiting for CI",
		SummarySetAt: &sixMinAgo,
		SummaryTTL:   600,
		LastOutputAt: &recentOutput,
	}
	cfg := &config.Config{}

	info := toSessionInfo(s, cfg, nil)
	if info.SummaryText != "Waiting for CI" {
		t.Errorf("SummaryText = %q, want %q — custom TTL should keep it fresh", info.SummaryText, "Waiting for CI")
	}

	if info.SummaryFaded {
		t.Error("should not be faded within custom TTL")
	}
}

func TestResolveSummary_LastOutputAt(t *testing.T) {
	out := time.Now().Add(-2 * time.Minute)
	s := SessionState{
		Status:       StatusRunning,
		LastOutputAt: &out,
	}
	cfg := &config.Config{}

	info := toSessionInfo(s, cfg, nil)
	if info.LastOutputAt == "" {
		t.Error("LastOutputAt should be populated")
	}
}

func TestResolveSummary_LastOutputAtFallback(t *testing.T) {
	created := time.Now().Add(-1 * time.Hour)
	changed := time.Now().Add(-30 * time.Minute)
	s := SessionState{
		Status:          StatusStopped,
		CreatedAt:       created,
		StatusChangedAt: changed,
	}
	cfg := &config.Config{}

	info := toSessionInfo(s, cfg, nil)
	if info.LastOutputAt == "" {
		t.Error("LastOutputAt should fall back to StatusChangedAt")
	}
}
