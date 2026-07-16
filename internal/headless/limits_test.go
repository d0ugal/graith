package headless

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIntOrDefault(t *testing.T) {
	cases := []struct {
		n, def, want int
	}{
		{0, 42, 42},
		{-5, 42, 42},
		{7, 42, 7},
	}
	for _, c := range cases {
		if got := intOrDefault(c.n, c.def); got != c.want {
			t.Errorf("intOrDefault(%d, %d) = %d, want %d", c.n, c.def, got, c.want)
		}
	}
}

func TestDurationOrDefault(t *testing.T) {
	def := 3 * time.Second

	cases := []struct {
		in, want time.Duration
	}{
		{0, def},
		{-1, def},
		{time.Second, time.Second},
	}
	for _, c := range cases {
		if got := durationOrDefault(c.in, def); got != c.want {
			t.Errorf("durationOrDefault(%v, %v) = %v, want %v", c.in, def, got, c.want)
		}
	}
}

// TestNewResolvesLimitDefaults confirms a session launched with unset Opts
// limits falls back to the package defaults (issue #1250), preserving the
// historical behaviour for direct callers.
func TestNewResolvesLimitDefaults(t *testing.T) {
	dir := t.TempDir()

	s, err := New(Opts{
		ID:      "canny",
		Command: "sh",
		Args:    []string{"-c", "exit 0"},
		Dir:     dir,
		LogPath: filepath.Join(dir, "scrollback.log"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(s.Close)

	if s.maxLineBytes != defaultMaxLineBytes {
		t.Errorf("maxLineBytes = %d, want default %d", s.maxLineBytes, defaultMaxLineBytes)
	}

	if s.controlTimeout != defaultControlTimeout {
		t.Errorf("controlTimeout = %v, want default %v", s.controlTimeout, defaultControlTimeout)
	}

	if s.interruptTimeout != defaultInterruptControlTimeout {
		t.Errorf("interruptTimeout = %v, want default %v", s.interruptTimeout, defaultInterruptControlTimeout)
	}

	if s.previewBytes != defaultScreenPreviewBytes {
		t.Errorf("previewBytes = %d, want default %d", s.previewBytes, defaultScreenPreviewBytes)
	}
}

// TestNewHonoursExplicitLimits confirms explicit Opts limits are threaded onto
// the session.
func TestNewHonoursExplicitLimits(t *testing.T) {
	dir := t.TempDir()

	s, err := New(Opts{
		ID:               "thrawn",
		Command:          "sh",
		Args:             []string{"-c", "exit 0"},
		Dir:              dir,
		LogPath:          filepath.Join(dir, "scrollback.log"),
		MaxLineBytes:     4096,
		ControlTimeout:   12 * time.Second,
		InterruptTimeout: 3 * time.Second,
		PreviewBytes:     2048,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(s.Close)

	if s.maxLineBytes != 4096 {
		t.Errorf("maxLineBytes = %d, want 4096", s.maxLineBytes)
	}

	if s.controlTimeout != 12*time.Second {
		t.Errorf("controlTimeout = %v, want 12s", s.controlTimeout)
	}

	if s.interruptTimeout != 3*time.Second {
		t.Errorf("interruptTimeout = %v, want 3s", s.interruptTimeout)
	}

	if s.previewBytes != 2048 {
		t.Errorf("previewBytes = %d, want 2048", s.previewBytes)
	}
}
