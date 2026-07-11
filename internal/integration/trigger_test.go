//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// TestScheduleTriggerFires spins up a daemon with a 1s-interval schedule trigger
// whose message action publishes to a topic, and asserts the daemon fires it.
// This exercises the schedule loop, run-state machine, action executor, and
// delivery end-to-end (in CI, where the daemon socket works).
func TestScheduleTriggerFires(t *testing.T) {
	env := setup(t, func(cfg *config.Config) {
		cfg.Triggers = []config.TriggerConfig{{
			Name:     "braw-tick",
			Schedule: &config.ScheduleConfig{Every: "1s"},
			Action: config.ActionConfig{
				Type:    config.ActionMessage,
				Body:    "tick {name}",
				Deliver: config.DeliverConfig{Topic: "kirk"},
			},
		}}
	})
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	// Poll the topic until the trigger fires (or time out).
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		sendControl(t, w, "msg_sub", protocol.MsgSubMsg{Stream: "kirk"})

		got := false
		for {
			resp := readControl(t, r)
			if resp.Type == "msg_message" {
				got = true
				continue
			}
			if resp.Type == "msg_done" || resp.Type == "error" {
				break
			}
		}

		if got {
			return // fired
		}

		time.Sleep(500 * time.Millisecond)
	}

	t.Fatal("schedule trigger did not fire within timeout")
}

// TestTriggerListControl exercises the trigger_list control message end-to-end.
func TestTriggerListControl(t *testing.T) {
	env := setup(t, func(cfg *config.Config) {
		cfg.Triggers = []config.TriggerConfig{{
			Name:     "canny-report",
			Schedule: &config.ScheduleConfig{Cron: "0 9 * * *"},
			Action:   config.ActionConfig{Type: config.ActionMessage, Body: "hi", Deliver: config.DeliverConfig{Topic: "t"}},
		}}
	})
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "trigger_list", protocol.TriggerListMsg{})
	resp := readControl(t, r)
	if resp.Type != "trigger_list" {
		t.Fatalf("expected trigger_list, got %s", resp.Type)
	}

	var listResp protocol.TriggerListResponse
	protocol.DecodePayload(resp, &listResp)

	if len(listResp.Triggers) != 1 || listResp.Triggers[0].Name != "canny-report" {
		t.Fatalf("unexpected triggers: %+v", listResp.Triggers)
	}
	if listResp.Triggers[0].Source != "schedule" {
		t.Errorf("source = %q, want schedule", listResp.Triggers[0].Source)
	}
}
