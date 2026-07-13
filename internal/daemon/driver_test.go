package daemon

import (
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestResumeRefusesHeadless locks in that a one-shot headless session cannot be
// resumed (issue #1075): resuming it would silently relaunch an interactive PTY
// while leaving DriverKind=headless, which the attach guard would then lock out.
func TestResumeRefusesHeadless(t *testing.T) {
	sm := &SessionManager{
		state: NewState(),
		cfg:   &config.Config{GitHubUsername: "ken"}, // non-empty: skip git discovery
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	sm.state.Sessions["braw"] = &SessionState{
		ID:         "braw",
		Name:       "braw",
		Status:     StatusStopped,
		DriverKind: DriverHeadless,
	}

	if _, err := sm.Resume("braw", 24, 80); err == nil || !strings.Contains(err.Error(), "headless") {
		t.Fatalf("Resume of a headless session should be refused, got err=%v", err)
	}
}

func TestResolveDriverKind(t *testing.T) {
	capable := config.Agent{HeadlessCapable: boolPtr(true)}
	incapable := config.Agent{HeadlessCapable: boolPtr(false)}
	on := config.HeadlessConfig{Experimental: true}
	onByDefault := config.HeadlessConfig{Experimental: true, Default: true}
	offByDefault := config.HeadlessConfig{Experimental: false, Default: true}

	tests := []struct {
		name      string
		explicit  bool
		agent     config.Agent
		hc        config.HeadlessConfig
		sandboxed bool
		want      string
		wantErr   bool
	}{
		{
			name: "no request, no default -> pty",
			want: DriverPTY,
		},
		{
			name:     "explicit but experimental off -> error",
			explicit: true, agent: capable, hc: config.HeadlessConfig{},
			wantErr: true,
		},
		{
			name:     "explicit, capable, experimental on -> headless",
			explicit: true, agent: capable, hc: on,
			want: DriverHeadless,
		},
		{
			name:     "explicit but agent not capable -> error",
			explicit: true, agent: incapable, hc: on,
			wantErr: true,
		},
		{
			name:     "explicit but sandboxed -> error",
			explicit: true, agent: capable, hc: on, sandboxed: true,
			wantErr: true,
		},
		{
			name:  "default preference, experimental off -> pty (soft yield, no error)",
			agent: capable, hc: offByDefault,
			want: DriverPTY,
		},
		{
			name:  "default preference, not capable -> pty (soft yield, no error)",
			agent: incapable, hc: onByDefault,
			want: DriverPTY,
		},
		{
			name:  "default preference, capable, experimental on -> headless",
			agent: capable, hc: onByDefault,
			want: DriverHeadless,
		},
		{
			name:  "default preference, sandboxed -> pty (soft yield, no error)",
			agent: capable, hc: onByDefault, sandboxed: true,
			want: DriverPTY,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveDriverKind(tt.explicit, tt.agent, tt.hc, tt.sandboxed)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got kind=%q nil err", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHeadlessCapableEnabled(t *testing.T) {
	if (config.Agent{}).HeadlessCapableEnabled() {
		t.Fatal("unset headless_capable should be false")
	}

	if !(config.Agent{HeadlessCapable: boolPtr(true)}).HeadlessCapableEnabled() {
		t.Fatal("headless_capable=true should be true")
	}

	if (config.Agent{HeadlessCapable: boolPtr(false)}).HeadlessCapableEnabled() {
		t.Fatal("headless_capable=false should be false")
	}
}

func TestHeadlessArgs(t *testing.T) {
	// "braw" stands in for the task prompt; agent args carry the session id.
	got := headlessArgs([]string{"--session-id", "canny"}, "review the braw diff")
	want := []string{
		"-p", "review the braw diff",
		"--output-format", "stream-json", "--verbose",
		"--session-id", "canny",
	}

	if !slices.Equal(got, want) {
		t.Fatalf("headlessArgs = %v, want %v", got, want)
	}

	// The prompt must come through as a single argv element (never re-split),
	// so a prompt with spaces stays intact.
	if got[1] != "review the braw diff" {
		t.Fatalf("prompt arg = %q, want it intact", got[1])
	}
}
