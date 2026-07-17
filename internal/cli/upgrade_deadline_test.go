package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// TestExecUpgradeFailsWhenSetDeadlineFails proves execUpgrade propagates a
// SetDeadline error and closes the connection instead of proceeding with an
// unbounded handshake that could hang (issue #1242).
func TestExecUpgradeFailsWhenSetDeadlineFails(t *testing.T) {
	origCfg, origNow, origDial := cfg, connectionNow, dialUpgradeClient

	t.Cleanup(func() { cfg, connectionNow, dialUpgradeClient = origCfg, origNow, origDial })

	cfg = &config.Config{Connection: config.ConnectionConfig{HandshakeTimeout: "1s"}}
	connectionNow = func() time.Time { return time.Unix(1_700_000, 0) }

	fake := &fakeUpgradeConn{deadlineErr: errors.New("cannot set deadline")}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	err := execUpgrade("done")
	if err == nil {
		t.Fatal("expected execUpgrade to fail when the deadline can't be installed")
	}

	if !fake.closed {
		t.Error("execUpgrade must close the connection when the deadline install fails")
	}

	if len(fake.sent) != 0 {
		t.Errorf("no control frames should be sent after a failed deadline install, got %v", fake.sent)
	}
}
