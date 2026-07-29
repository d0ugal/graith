package daemon

import "testing"

func TestNativeTranscriptRootForLaunchKeepsCapturedRoot(t *testing.T) {
	got := nativeTranscriptRootForLaunch("codex", "braw-id", "/hame/new-codex", "/hame/old-codex")
	if got != "/hame/old-codex" {
		t.Fatalf("root = %q, want existing captured root", got)
	}
}

func TestNativeTranscriptRootForLaunchUsesLaunchRootBeforeCapture(t *testing.T) {
	got := nativeTranscriptRootForLaunch("codex", "", "/hame/new-codex", "/hame/old-codex")
	if got != "/hame/new-codex" {
		t.Fatalf("root = %q, want launch root", got)
	}
}
