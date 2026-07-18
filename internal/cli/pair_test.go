package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
)

func setOutBufForPairings(t *testing.T, jsonMode bool) *bytes.Buffer {
	t.Helper()

	orig := out
	t.Cleanup(func() { out = orig })

	var buf bytes.Buffer
	out = output.NewWithWriter(jsonMode, &buf)

	return &buf
}

func TestRemotePairingsCommandTree(t *testing.T) {
	registerCommands()

	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "pair" {
			t.Fatal("root command still contains removed gr pair namespace")
		}
		for _, alias := range cmd.Aliases {
			if alias == "pair" {
				t.Fatalf("root command %q retains removed gr pair alias", cmd.Name())
			}
		}
	}

	wantPaths := map[string]string{
		"list":    "gr remote pairings list",
		"approve": "gr remote pairings approve",
		"revoke":  "gr remote pairings revoke",
	}
	for _, cmd := range remotePairingsCmd.Commands() {
		if cmd.Name() == "help" {
			continue
		}

		want, ok := wantPaths[cmd.Name()]
		if !ok {
			t.Errorf("unexpected gr remote pairings subcommand %q", cmd.Name())
			continue
		}

		if got := cmd.CommandPath(); got != want {
			t.Errorf("%s command path = %q, want %q", cmd.Name(), got, want)
		}

		delete(wantPaths, cmd.Name())
	}

	for missing := range wantPaths {
		t.Errorf("gr remote pairings %s is not registered", missing)
	}

	if got := remotePairCmd.CommandPath(); got != "gr remote pair" {
		t.Errorf("pair initiation command path = %q, want %q", got, "gr remote pair")
	}
}

func TestRemotePairingsBashCompletionNamespace(t *testing.T) {
	registerCommands()

	var buf bytes.Buffer
	if err := rootCmd.GenBashCompletion(&buf); err != nil {
		t.Fatalf("generate bash completion: %v", err)
	}

	script := buf.String()
	for _, fn := range []string{
		"_gr_remote_pairings()",
		"_gr_remote_pairings_list()",
		"_gr_remote_pairings_approve()",
		"_gr_remote_pairings_revoke()",
	} {
		if !strings.Contains(script, fn) {
			t.Errorf("completion script missing %q", fn)
		}
	}

	for _, removed := range []string{"_gr_pair()", "_gr_pair_list()", "_gr_pair_approve()", "_gr_pair_revoke()"} {
		if strings.Contains(script, removed) {
			t.Errorf("completion script retains removed root function %q", removed)
		}
	}
}

func TestRemotePairingsHelpNamespace(t *testing.T) {
	registerCommands()

	var rootHelp bytes.Buffer
	rootCmd.SetOut(&rootHelp)
	t.Cleanup(func() { rootCmd.SetOut(nil) })

	if err := rootCmd.Help(); err != nil {
		t.Fatalf("render root help: %v", err)
	}
	if strings.Contains(rootHelp.String(), "\n  pair ") {
		t.Errorf("root help retains removed gr pair command:\n%s", rootHelp.String())
	}

	var remoteHelp bytes.Buffer
	remoteCmd.SetOut(&remoteHelp)
	t.Cleanup(func() { remoteCmd.SetOut(nil) })

	if err := remoteCmd.Help(); err != nil {
		t.Fatalf("render remote help: %v", err)
	}
	if !strings.Contains(remoteHelp.String(), "pairings") ||
		!strings.Contains(remoteHelp.String(), "Manage remote device pairings") {
		t.Errorf("remote help does not advertise pairing administration:\n%s", remoteHelp.String())
	}
}

func TestWritePairingsPreservesHumanOutput(t *testing.T) {
	buf := setOutBufForPairings(t, false)
	pl := protocol.PairListResponseMsg{
		Pending: []protocol.PairPending{{
			RequestID: "req-braw", DeviceLabel: "canny phone", TailnetUser: "bairn@example.com",
			TailnetNode: "dreich", RequestedAt: "2026-07-18T10:00:00Z",
		}},
		Paired: []protocol.PairedDeviceInfo{{
			DeviceID: "dev-croft", Label: "bothy tablet", TailnetUser: "bairn@example.com",
			TailnetNode: "thrawn", CreatedAt: "2026-07-17T09:00:00Z",
		}},
	}

	if err := writePairings(pl); err != nil {
		t.Fatalf("write pairings: %v", err)
	}

	want := "Pending pairing requests:\n" +
		"  req-braw  \"canny phone\"  bairn@example.com (dreich)  requested 2026-07-18T10:00:00Z\n" +
		"Paired devices:\n" +
		"  dev-croft  \"bothy tablet\"  bairn@example.com (thrawn)  paired 2026-07-17T09:00:00Z\n"
	if got := buf.String(); got != want {
		t.Errorf("human pairing list output = %q, want %q", got, want)
	}
}

func TestWritePairingsPreservesJSONContract(t *testing.T) {
	buf := setOutBufForPairings(t, true)
	pl := protocol.PairListResponseMsg{
		Pending: []protocol.PairPending{{RequestID: "req-braw", DeviceLabel: "canny"}},
		Paired:  []protocol.PairedDeviceInfo{{DeviceID: "dev-croft", Label: "bothy"}},
	}

	if err := writePairings(pl); err != nil {
		t.Fatalf("write pairings: %v", err)
	}

	var got protocol.PairListResponseMsg
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode pairing list JSON: %v", err)
	}

	if len(got.Pending) != 1 || got.Pending[0].RequestID != "req-braw" ||
		len(got.Paired) != 1 || got.Paired[0].DeviceID != "dev-croft" {
		t.Errorf("pairing list JSON = %+v, want existing pending/paired shape", got)
	}
}

func TestWritePairingMutationPreservesHumanOutput(t *testing.T) {
	buf := setOutBufForPairings(t, false)

	writePairingApproval(protocol.PairResponseMsg{DeviceID: "dev-braw", TLSPinSPKI: "pin-canny"})
	writePairingRevocation("dev-thrawn")

	want := "Device paired: dev-braw\n" +
		"The device received its credentials over its pairing connection.\n" +
		"TLS SPKI pin: pin-canny\n" +
		"Device revoked: dev-thrawn\n"
	if got := buf.String(); got != want {
		t.Errorf("pairing mutation output = %q, want %q", got, want)
	}
}

// TestControlErrorCov2DecodesMessage confirms controlError surfaces the
// daemon's error message verbatim from an error envelope.
func TestControlErrorCov2DecodesMessage(t *testing.T) {
	raw, err := protocol.EncodeControl("error", protocol.ErrorMsg{Message: "the loch is thrawn"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	env, err := protocol.DecodeControl(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	got := controlError(env)
	if got == nil {
		t.Fatal("expected an error")
	}

	if got.Error() != "the loch is thrawn" {
		t.Errorf("controlError = %q, want %q", got.Error(), "the loch is thrawn")
	}
}

// TestControlErrorCov2EmptyPayload ensures a malformed/empty error envelope
// still yields a (blank-message) error rather than panicking — the decode
// failure is swallowed and the empty message is formatted.
func TestControlErrorCov2EmptyPayload(t *testing.T) {
	env := protocol.Envelope{Type: "error"}

	got := controlError(env)
	if got == nil {
		t.Fatal("expected a non-nil error even for an empty payload")
	}

	if got.Error() != "" {
		t.Errorf("controlError with empty payload = %q, want empty string", got.Error())
	}
}
