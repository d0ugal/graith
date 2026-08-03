package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeControl(t *testing.T) {
	handshake := HandshakeMsg{
		Version: "3.0", ClientID: "brig-client",
		TerminalSize: [2]uint16{80, 24}, Cwd: "/home/user/croft",
	}

	data, err := EncodeControl("handshake", handshake)
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}

	if msg.Type != "handshake" {
		t.Errorf("Type = %q, want handshake", msg.Type)
	}

	var got HandshakeMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.ClientID != "brig-client" {
		t.Errorf("ClientID = %q", got.ClientID)
	}

	if got.Cwd != "/home/user/croft" {
		t.Errorf("Cwd = %q", got.Cwd)
	}
}

func TestCreateMsgLabelsWirePresence(t *testing.T) {
	tests := map[string]struct {
		input     CreateMsg
		wantJSON  string
		wantOmit  string
		wantNil   bool
		wantLabel []string
	}{
		"nil labels are omitted and decode nil": {
			input:    CreateMsg{Name: "bairn", Agent: "codex", RepoPath: "/croft"},
			wantOmit: `"labels"`,
			wantNil:  true,
		},
		"empty labels encode explicit empty array": {
			input:     CreateMsg{Name: "bairn", Labels: &[]string{}, Agent: "codex", RepoPath: "/croft"},
			wantJSON:  `"labels":[]`,
			wantLabel: []string{},
		},
		"non-empty labels encode explicit set": {
			input:     CreateMsg{Name: "bairn", Labels: &[]string{"strath"}, Agent: "codex", RepoPath: "/croft"},
			wantJSON:  `"labels":["strath"]`,
			wantLabel: []string{"strath"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(test.input)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			if test.wantJSON != "" && !strings.Contains(string(data), test.wantJSON) {
				t.Fatalf("CreateMsg JSON = %s, want it to contain %s", data, test.wantJSON)
			}

			if test.wantOmit != "" && strings.Contains(string(data), test.wantOmit) {
				t.Fatalf("CreateMsg JSON = %s, want it to omit %s", data, test.wantOmit)
			}

			var got CreateMsg
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if test.wantNil {
				if got.Labels != nil {
					t.Fatalf("Labels = %#v, want nil", got.Labels)
				}

				return
			}

			if got.Labels == nil {
				t.Fatalf("Labels = nil, want %#v", test.wantLabel)
			}

			if !reflect.DeepEqual(*got.Labels, test.wantLabel) {
				t.Fatalf("Labels = %#v, want %#v", *got.Labels, test.wantLabel)
			}
		})
	}

	var omitted CreateMsg
	if err := json.Unmarshal([]byte(`{"name":"bairn","agent":"codex","repo_path":"/croft"}`), &omitted); err != nil {
		t.Fatalf("Unmarshal omitted labels: %v", err)
	}

	if omitted.Labels != nil {
		t.Fatalf("omitted labels decode to %#v, want nil", omitted.Labels)
	}
}

func TestAgentCatalogResponseWireShape(t *testing.T) {
	input := AgentCatalogResponseMsg{
		Agents: []AgentCatalogEntry{
			{Name: "thrawn", Command: "thrawn-cli", InfoKeys: []string{"model", "version"}},
			{Name: "strath", Command: "strath-cli"},
		},
		DefaultAgent: "thrawn",
	}

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	for _, want := range []string{
		`"name":"thrawn"`,
		`"command":"thrawn-cli"`,
		`"info_keys":["model","version"]`,
		`"default_agent":"thrawn"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("AgentCatalogResponseMsg JSON = %s, want it to contain %s", data, want)
		}
	}

	var got AgentCatalogResponseMsg
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.DefaultAgent != "thrawn" {
		t.Fatalf("DefaultAgent = %q, want thrawn", got.DefaultAgent)
	}

	if len(got.Agents) != 2 || got.Agents[0].Name != "thrawn" {
		t.Fatalf("Agents = %#v, want custom thrawn first", got.Agents)
	}

	if !reflect.DeepEqual(got.Agents[0].InfoKeys, []string{"model", "version"}) {
		t.Fatalf("InfoKeys = %#v, want [model version]", got.Agents[0].InfoKeys)
	}
}

func TestTriggerBindingDetailWatcherUsageJSON(t *testing.T) {
	detail := TriggerBindingDetail{
		SessionID:                    "braw",
		State:                        "idle",
		RegisteredWatchDirectories:   12,
		LiveWatchDirectories:         10,
		StaleWatchDirectories:        2,
		EstimatedWatchDescriptorCost: 34,
		LiveEstimatedWatchCost:       28,
		StaleEstimatedWatchCost:      6,
		WatchBudgetPercent:           4.25,
	}

	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	tests := map[string]float64{
		"registered_watch_directories":    12,
		"live_watch_directories":          10,
		"stale_watch_directories":         2,
		"estimated_watch_descriptor_cost": 34,
		"live_estimated_watch_cost":       28,
		"stale_estimated_watch_cost":      6,
		"watch_budget_percent":            4.25,
	}

	for field, want := range tests {
		if got[field] != want {
			t.Fatalf("%s = %#v in %s, want %v", field, got[field], data, want)
		}
	}
}

func TestTriggerStatusResponseRunCountJSON(t *testing.T) {
	tests := map[string]struct {
		runCount int
	}{
		"zero run count is present":     {runCount: 0},
		"non-zero run count is present": {runCount: 7},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(TriggerStatusResponse{
				Trigger: TriggerRecord{
					Name: "braw", Source: "schedule", Action: "message",
					Enabled: true, RunCount: test.runCount,
				},
			})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var got struct {
				Trigger struct {
					RunCount *int `json:"run_count"`
				} `json:"trigger"`
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if got.Trigger.RunCount == nil {
				t.Fatalf("TriggerStatusResponse JSON = %s, want run_count field", data)
			}

			if *got.Trigger.RunCount != test.runCount {
				t.Fatalf("run_count = %d, want %d in %s", *got.Trigger.RunCount, test.runCount, data)
			}
		})
	}
}

func TestCreateMsgReadOnlyRoundTrip(t *testing.T) {
	want := CreateMsg{
		Name: "reader", Agent: "codex", RepoPath: "/croft", Base: "main", ReadOnly: true,
	}

	data, err := EncodeControl("create", want)
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}

	var got CreateMsg
	if err := DecodePayload(envelope, &got); err != nil {
		t.Fatal(err)
	}

	if !got.ReadOnly || got.Base != "main" || got.RepoPath != "/croft" {
		t.Fatalf("CreateMsg = %+v", got)
	}
}

func TestMsgPubNoReplyRoundTrip(t *testing.T) {
	want := MsgPubMsg{
		Stream: "updates", Body: "morning briefing complete",
		SenderID: "braw-sender", SenderName: "Braw", NoReply: true,
	}

	data, err := EncodeControl("msg_pub", want)
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}

	env, err := DecodeControl(data)
	if err != nil {
		t.Fatalf("DecodeControl: %v", err)
	}

	var got MsgPubMsg
	if err := DecodePayload(env, &got); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}

	if !got.NoReply {
		t.Errorf("NoReply = false, want true")
	}

	defaultData, err := EncodeControl("msg_pub", MsgPubMsg{Stream: "updates", Body: "replyable"})
	if err != nil {
		t.Fatalf("EncodeControl default: %v", err)
	}

	if strings.Contains(string(defaultData), "no_reply") {
		t.Errorf("default no_reply should be omitted for backward compatibility: %s", defaultData)
	}
}

func TestScenarioResultPublishRoundTrip(t *testing.T) {
	want := ScenarioResultPublishMsg{
		Scenario: "braw-fanout", Name: "review", Body: "# Canny review",
	}

	data, err := EncodeControl("scenario_result_publish", want)
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}

	env, err := DecodeControl(data)
	if err != nil {
		t.Fatalf("DecodeControl: %v", err)
	}

	var got ScenarioResultPublishMsg
	if err := DecodePayload(env, &got); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}

	if got != want {
		t.Fatalf("publish = %+v, want %+v", got, want)
	}

	response := ScenarioResultPublishResponse{
		Scenario: "braw-fanout",
		Member:   "canny",
		Result: ScenarioResultInfo{
			Name: "review", Format: "markdown", Required: true,
			Destination: "scenarios/sc-braw/results/canny/review.md",
			Status:      "available", SizeBytes: 14,
		},
	}

	data, err = EncodeControl("scenario_result_published", response)
	if err != nil {
		t.Fatalf("EncodeControl response: %v", err)
	}

	env, err = DecodeControl(data)
	if err != nil {
		t.Fatalf("DecodeControl response: %v", err)
	}

	var responseGot ScenarioResultPublishResponse
	if err := DecodePayload(env, &responseGot); err != nil {
		t.Fatalf("DecodePayload response: %v", err)
	}

	if responseGot.Result.Status != "available" || responseGot.Result.Destination != response.Result.Destination {
		t.Fatalf("response = %+v", responseGot)
	}
}

func TestVersionCompatible(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"same version", Version, true},
		{"same major different minor", "3.99", true},
		{"different major", "1.0", false},
		{"no dot", "1", false},
		{"empty string", "", false},
		{"major only with dot", "3.", true},
		{"three part version", "3.2.3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VersionCompatible(tt.version); got != tt.want {
				t.Errorf("VersionCompatible(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestSessionInfoRoundTrip(t *testing.T) {
	session := SessionInfo{
		ID: "a3f2b1c9", Name: "braw-auth-fix", RepoPath: "/home/user/croft",
		RepoName: "croft", Branch: "d0ugal/graith/braw-auth-fix-a3f2b1c9",
		Agent: "claude", Status: "running", Labels: []string{"Urgent", "release"},
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := EncodeControl("session_update", session)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got SessionInfo
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}

	if got.ID != "a3f2b1c9" || got.Name != "braw-auth-fix" ||
		!reflect.DeepEqual(got.Labels, []string{"Urgent", "release"}) {
		t.Errorf("session = %+v", got)
	}
}

func TestPairRequestRoundTrip(t *testing.T) {
	req := PairRequestMsg{DeviceLabel: "bairn", DevicePubKey: "ed25519-pubkey-abc"}

	data, err := EncodeControl("pair_request", req)
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}

	if msg.Type != "pair_request" {
		t.Errorf("Type = %q, want pair_request", msg.Type)
	}

	var got PairRequestMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.DeviceLabel != "bairn" || got.DevicePubKey != "ed25519-pubkey-abc" {
		t.Errorf("pair request = %+v", got)
	}
}

func TestPairResponseRoundTrip(t *testing.T) {
	resp := PairResponseMsg{
		DeviceID: "dev-skelf", ClientToken: "tok-croft",
		DaemonProfile: "default", TLSPinSPKI: "spki-pin-xyz",
	}

	data, err := EncodeControl("pair_response", resp)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got PairResponseMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.DeviceID != "dev-skelf" || got.ClientToken != "tok-croft" ||
		got.DaemonProfile != "default" || got.TLSPinSPKI != "spki-pin-xyz" {
		t.Errorf("pair response = %+v", got)
	}
}

func TestPairApproveRoundTrip(t *testing.T) {
	approve := PairApproveMsg{RequestID: "req-speir"}

	data, err := EncodeControl("pair_approve", approve)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got PairApproveMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.RequestID != "req-speir" {
		t.Errorf("pair approve = %+v", got)
	}
}

func TestPairListResponseRoundTrip(t *testing.T) {
	resp := PairListResponseMsg{
		Pending: []PairPending{{
			RequestID: "req-bairn", DeviceLabel: "bairn",
			TailnetUser: "speir@example.com", TailnetNode: "node-croft",
			RequestedAt: "2026-07-07T10:00:00Z",
		}},
		Paired: []PairedDeviceInfo{{
			DeviceID: "dev-skelf", Label: "skelf",
			TailnetUser: "speir@example.com", TailnetNode: "node-bothy",
			CreatedAt: "2026-07-06T09:00:00Z", LastSeenAt: "2026-07-07T09:00:00Z",
		}},
	}

	data, err := EncodeControl("pair_list_response", resp)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got PairListResponseMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Pending) != 1 || got.Pending[0].DeviceLabel != "bairn" ||
		got.Pending[0].TailnetUser != "speir@example.com" {
		t.Errorf("pending = %+v", got.Pending)
	}

	if len(got.Paired) != 1 || got.Paired[0].Label != "skelf" ||
		got.Paired[0].LastSeenAt != "2026-07-07T09:00:00Z" {
		t.Errorf("paired = %+v", got.Paired)
	}
}

func TestPairRevokeRoundTrip(t *testing.T) {
	revoke := PairRevokeMsg{DeviceID: "dev-thrawn"}

	data, err := EncodeControl("pair_revoke", revoke)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got PairRevokeMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.DeviceID != "dev-thrawn" {
		t.Errorf("pair revoke = %+v", got)
	}
}

func TestAuthChallengeRoundTrip(t *testing.T) {
	chal := AuthChallengeMsg{Nonce: "nonce-haar"}

	data, err := EncodeControl("auth_challenge", chal)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got AuthChallengeMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.Nonce != "nonce-haar" {
		t.Errorf("auth challenge = %+v", got)
	}
}

func TestAuthProofRoundTrip(t *testing.T) {
	proof := AuthProofMsg{DeviceID: "dev-skelf", Signature: "sig-bairn"}

	data, err := EncodeControl("auth_proof", proof)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got AuthProofMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.DeviceID != "dev-skelf" || got.Signature != "sig-bairn" {
		t.Errorf("auth proof = %+v", got)
	}
}

func TestRepoListResponseRoundTrip(t *testing.T) {
	resp := RepoListResponseMsg{
		Repos: []RepoEntry{
			{Path: "/home/user/croft", Name: "croft", Recent: true},
			{Path: "/home/user/bothy", Name: "bothy"},
		},
	}

	data, err := EncodeControl("repo_list_response", resp)
	if err != nil {
		t.Fatal(err)
	}

	msg, _ := DecodeControl(data)

	var got RepoListResponseMsg
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Repos) != 2 {
		t.Fatalf("repos = %+v", got.Repos)
	}

	if got.Repos[0].Name != "croft" || !got.Repos[0].Recent {
		t.Errorf("repo[0] = %+v", got.Repos[0])
	}

	if got.Repos[1].Name != "bothy" || got.Repos[1].Recent {
		t.Errorf("repo[1] = %+v", got.Repos[1])
	}
}

func TestScenarioMirrorRoundTrip(t *testing.T) {
	want := ScenarioStatusResponse{Scenario: ScenarioRecord{
		ID: "sc-braw", Name: "strath-readers", Sessions: []ScenarioSessionInfo{
			{Name: "reader", SessionID: "canny-reader", Mirror: "subject", Status: "running"},
			{Name: "branch-reader", SessionID: "bothy-reader", ReadOnly: true, Status: "running"},
		},
	}}

	data, err := EncodeControl("scenario_status", want)
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}

	var got ScenarioStatusResponse
	if err := DecodePayload(msg, &got); err != nil {
		t.Fatal(err)
	}

	if got.Scenario.Sessions[0].Mirror != "subject" {
		t.Errorf("mirror = %q, want subject", got.Scenario.Sessions[0].Mirror)
	}

	if !got.Scenario.Sessions[1].ReadOnly {
		t.Errorf("read_only = false, want true")
	}
}

func TestScenarioStartupPromptRoundTrip(t *testing.T) {
	input := ScenarioSessionInput{
		Name: "canny", Prompt: "Inspect the croft in detail.", Task: "publish the report",
	}

	data, err := EncodeControl("scenario_add", ScenarioAddMsg{Name: "strath", Session: input})
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}

	var got ScenarioAddMsg
	if err := DecodePayload(envelope, &got); err != nil {
		t.Fatal(err)
	}

	if got.Session.Prompt != input.Prompt || got.Session.Task != input.Task || got.Session.StartupPrompt() != input.Prompt {
		t.Fatalf("round trip session = %+v", got.Session)
	}

	if fallback := (ScenarioSessionInput{Task: "legacy task"}).StartupPrompt(); fallback != "legacy task" {
		t.Fatalf("task-only startup prompt = %q", fallback)
	}

	if fallback := (ScenarioSessionInput{Prompt: " \n\t", Task: "legacy task"}).StartupPrompt(); fallback != "legacy task" {
		t.Fatalf("whitespace-only prompt fallback = %q", fallback)
	}

	if shared := (ScenarioSessionInput{Shared: true, Task: "tracked elsewhere"}).StartupPrompt(); shared != "" {
		t.Fatalf("shared startup prompt = %q, want empty", shared)
	}
}
