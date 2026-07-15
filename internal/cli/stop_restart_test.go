package cli

import (
	"strings"
	"testing"
)

// The Args validators read package-global flags (stopChildren/stopBatch,
// restartChildren). Save and restore them around each case.

func withStopFlagsCov2(t *testing.T, children bool, batch batchFlags, fn func()) {
	t.Helper()

	prevChildren, prevBatch := stopChildren, stopBatch
	stopChildren, stopBatch = children, batch

	defer func() { stopChildren, stopBatch = prevChildren, prevBatch }()

	fn()
}

func withRestartChildrenCov2(t *testing.T, children bool, fn func()) {
	t.Helper()

	prev := restartChildren
	restartChildren = children

	defer func() { restartChildren = prev }()

	fn()
}

func TestStopArgsCov2ChildrenAndBatchRejected(t *testing.T) {
	withStopFlagsCov2(t, true, batchFlags{stopped: true}, func() {
		err := stopCmd.Args(stopCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("expected --children+batch rejection, got %v", err)
		}
	})
}

func TestStopArgsCov2BatchTakesNoArgs(t *testing.T) {
	withStopFlagsCov2(t, false, batchFlags{stopped: true}, func() {
		if err := stopCmd.Args(stopCmd, nil); err != nil {
			t.Errorf("batch mode with no args should be allowed: %v", err)
		}

		if err := stopCmd.Args(stopCmd, []string{"braw"}); err == nil {
			t.Error("batch mode with a positional arg should be rejected")
		}
	})
}

func TestStopArgsCov2ChildrenAllowsZeroOrOne(t *testing.T) {
	withStopFlagsCov2(t, true, batchFlags{}, func() {
		if err := stopCmd.Args(stopCmd, nil); err != nil {
			t.Errorf("--children with no arg should be allowed: %v", err)
		}

		if err := stopCmd.Args(stopCmd, []string{"braw"}); err != nil {
			t.Errorf("--children with one arg should be allowed: %v", err)
		}

		if err := stopCmd.Args(stopCmd, []string{"braw", "canny"}); err == nil {
			t.Error("--children with two args should be rejected")
		}
	})
}

func TestStopArgsCov2DefaultRequiresExactlyOne(t *testing.T) {
	withStopFlagsCov2(t, false, batchFlags{}, func() {
		if err := stopCmd.Args(stopCmd, nil); err == nil {
			t.Error("plain stop with no arg should be rejected")
		}

		if err := stopCmd.Args(stopCmd, []string{"braw"}); err != nil {
			t.Errorf("plain stop with one arg should be allowed: %v", err)
		}
	})
}

func TestStopArgsSelf(t *testing.T) {
	prevSelf := stopSelf
	stopSelf = true

	defer func() { stopSelf = prevSelf }()

	// --self alone: no positional arg allowed.
	withStopFlagsCov2(t, false, batchFlags{}, func() {
		if err := stopCmd.Args(stopCmd, nil); err != nil {
			t.Errorf("--self with no arg should be allowed: %v", err)
		}

		if err := stopCmd.Args(stopCmd, []string{"braw"}); err == nil {
			t.Error("--self with a positional arg should be rejected")
		}
	})

	// --self with --children is contradictory.
	withStopFlagsCov2(t, true, batchFlags{}, func() {
		if err := stopCmd.Args(stopCmd, nil); err == nil {
			t.Error("--self with --children should be rejected")
		}
	})

	// --self with a batch filter is contradictory.
	withStopFlagsCov2(t, false, batchFlags{stopped: true}, func() {
		if err := stopCmd.Args(stopCmd, nil); err == nil {
			t.Error("--self with a batch filter should be rejected")
		}
	})
}

func TestRestartArgsCov2ChildrenAllowsZeroOrOne(t *testing.T) {
	withRestartChildrenCov2(t, true, func() {
		if err := restartCmd.Args(restartCmd, nil); err != nil {
			t.Errorf("--children with no arg should be allowed: %v", err)
		}

		if err := restartCmd.Args(restartCmd, []string{"braw", "canny"}); err == nil {
			t.Error("--children with two args should be rejected")
		}
	})
}

func TestRestartArgsCov2DefaultRequiresExactlyOne(t *testing.T) {
	withRestartChildrenCov2(t, false, func() {
		if err := restartCmd.Args(restartCmd, nil); err == nil {
			t.Error("plain restart with no arg should be rejected")
		}

		if err := restartCmd.Args(restartCmd, []string{"braw"}); err != nil {
			t.Errorf("plain restart with one arg should be allowed: %v", err)
		}
	})
}

// stopChildrenRun / restartChildrenRun consult GRAITH_SESSION_ID only when no
// session arg is given; with it unset they return an error before ever using
// the client, so a nil client is safe here.
func TestStopChildrenRunCov2NoSessionEnv(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")

	err := stopChildrenRun(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "GRAITH_SESSION_ID") {
		t.Fatalf("expected GRAITH_SESSION_ID error, got %v", err)
	}
}

func TestRestartChildrenRunCov2NoSessionEnv(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")

	err := restartChildrenRun(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "GRAITH_SESSION_ID") {
		t.Fatalf("expected GRAITH_SESSION_ID error, got %v", err)
	}
}
