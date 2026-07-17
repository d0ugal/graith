package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
)

// writeRemoteHosts seeds a remote-hosts.json in a fresh data dir and points the
// package paths at it, returning the dir. hosts maps store key → RemoteHost.
func writeRemoteHosts(t *testing.T, hosts map[string]*client.RemoteHost) string {
	t.Helper()

	dir := t.TempDir()

	if hosts != nil {
		store := struct {
			Hosts map[string]*client.RemoteHost `json:"hosts"`
		}{Hosts: hosts}

		data, err := json.Marshal(store)
		if err != nil {
			t.Fatalf("marshal store: %v", err)
		}

		if err := os.WriteFile(client.RemoteHostsPath(dir), data, 0o600); err != nil {
			t.Fatalf("write store: %v", err)
		}
	}

	origPaths := paths

	t.Cleanup(func() { paths = origPaths })

	paths = config.Paths{DataDir: dir}

	return dir
}

func setOutBufForRemote(t *testing.T, jsonMode bool) *bytes.Buffer {
	t.Helper()

	orig := out

	t.Cleanup(func() { out = orig })

	var buf bytes.Buffer

	out = output.NewWithWriter(jsonMode, &buf)

	return &buf
}

// TestRemotePairPortDefault verifies the `gr remote pair --port` flag defaults
// to the centralized config.DefaultRemotePort constant rather than a duplicated
// literal (#1235).
func TestRemotePairPortDefault(t *testing.T) {
	registerCommands()

	flag := remotePairCmd.Flags().Lookup("port")
	if flag == nil {
		t.Fatal("remote pair command has no --port flag")
	}

	want := strconv.Itoa(config.DefaultRemotePort)
	if flag.DefValue != want {
		t.Errorf("--port default = %q, want config.DefaultRemotePort (%q)", flag.DefValue, want)
	}
}

// TestRemoteListEmpty verifies the list command prints the pairing hint when no
// remote hosts are stored.
func TestRemoteListEmpty(t *testing.T) {
	writeRemoteHosts(t, nil)
	buf := setOutBufForRemote(t, false)

	if err := remoteListCmd.RunE(remoteListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if !strings.Contains(buf.String(), "No paired remote hosts") {
		t.Errorf("output = %q, want the no-hosts hint", buf.String())
	}
}

// TestRemoteListWithHosts verifies each paired host is rendered in the text
// listing.
func TestRemoteListWithHosts(t *testing.T) {
	writeRemoteHosts(t, map[string]*client.RemoteHost{
		"ben.tailnet.ts.net":  {Host: "ben.tailnet.ts.net", Port: 4823, Profile: "kirk"},
		"brae.tailnet.ts.net": {Host: "brae.tailnet.ts.net", Port: 4824, Profile: ""},
	})
	buf := setOutBufForRemote(t, false)

	if err := remoteListCmd.RunE(remoteListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "ben.tailnet.ts.net") || !strings.Contains(got, "brae.tailnet.ts.net") {
		t.Errorf("output = %q, want both hosts listed", got)
	}
}

// TestRemoteListJSON verifies the JSON listing emits the host/port/profile rows.
func TestRemoteListJSON(t *testing.T) {
	writeRemoteHosts(t, map[string]*client.RemoteHost{
		"ben.tailnet.ts.net": {Host: "ben.tailnet.ts.net", Port: 4823, Profile: "kirk"},
	})
	buf := setOutBufForRemote(t, true)

	if err := remoteListCmd.RunE(remoteListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var rows []struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		Profile string `json:"profile"`
	}

	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal JSON output: %v", err)
	}

	if len(rows) != 1 || rows[0].Host != "ben.tailnet.ts.net" || rows[0].Port != 4823 || rows[0].Profile != "kirk" {
		t.Errorf("rows = %+v, want single ben row", rows)
	}
}

// TestRemoteAttachArgParseErrors verifies the <host>/<session> argument is
// validated before any store lookup.
func TestRemoteAttachArgParseErrors(t *testing.T) {
	setOutBufForRemote(t, false)

	for _, arg := range []string{"no-slash", "/braw", "ben/"} {
		if err := remoteAttachCmd.RunE(remoteAttachCmd, []string{arg}); err == nil {
			t.Errorf("arg %q: expected error, got nil", arg)
		}
	}
}

// TestRemoteAttachNoPairedHosts verifies a well-formed target against an empty
// store reports that nothing is paired.
func TestRemoteAttachNoPairedHosts(t *testing.T) {
	writeRemoteHosts(t, nil)
	setOutBufForRemote(t, false)

	err := remoteAttachCmd.RunE(remoteAttachCmd, []string{"ben/braw"})
	if err == nil || !strings.Contains(err.Error(), "no paired remote hosts") {
		t.Fatalf("err = %v, want no-paired-hosts error", err)
	}
}

// TestRemoteAttachUnknownHostLists verifies an unmatched host name yields a
// "not paired" error naming the available candidates.
func TestRemoteAttachUnknownHostLists(t *testing.T) {
	writeRemoteHosts(t, map[string]*client.RemoteHost{
		"canny.tailnet.ts.net": {Host: "canny.tailnet.ts.net", Port: 4823},
	})
	setOutBufForRemote(t, false)

	err := remoteAttachCmd.RunE(remoteAttachCmd, []string{"dreich/braw"})
	if err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("err = %v, want not-paired error", err)
	}
}

// TestRunRemoteAttachRejectedInsideGraith verifies remote attach refuses to run
// from within an existing graith session (which would nest PTYs).
func TestRunRemoteAttachRejectedInsideGraith(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "inside-ben")

	err := runRemoteAttach(&client.RemoteHost{Host: "ben.tailnet.ts.net"}, nil, "braw")
	if err == nil || !strings.Contains(err.Error(), "cannot attach from inside") {
		t.Fatalf("err = %v, want inside-graith rejection", err)
	}
}

// TestRemoteAttachResolvesThenRejectsInsideGraith drives the attach command
// through a successful host resolve and device-key load, stopping at the
// inside-graith guard so no network dial is attempted.
func TestRemoteAttachResolvesThenRejectsInsideGraith(t *testing.T) {
	writeRemoteHosts(t, map[string]*client.RemoteHost{
		"ben.tailnet.ts.net": {Host: "ben.tailnet.ts.net", Port: 4823},
	})
	setOutBufForRemote(t, false)
	t.Setenv("GRAITH_SESSION_ID", "inside-ben")

	// Short-name "ben" resolves to the fully-qualified key via prefix match.
	err := remoteAttachCmd.RunE(remoteAttachCmd, []string{"ben/braw"})
	if err == nil || !strings.Contains(err.Error(), "cannot attach from inside") {
		t.Fatalf("err = %v, want inside-graith rejection after resolve", err)
	}
}

// TestRemoteHostsPathLayout guards the on-disk store location the CLI relies on.
func TestRemoteHostsPathLayout(t *testing.T) {
	got := client.RemoteHostsPath("/glen/data")
	if want := filepath.Join("/glen/data", "remote-hosts.json"); got != want {
		t.Errorf("RemoteHostsPath = %q, want %q", got, want)
	}
}

// TestRunRemotePairKeepsConcurrentHostUpdate proves the receipt-critical save
// inside the pairing exchange is authoritative: a concurrent second-host update
// landing between that durable pre-ACK save and the command's return must not be
// clobbered, and no spurious post-success failure may be surfaced (issues #1299
// and #1330). Each callback-side update acquires the store lock independently,
// which also proves runRemotePair released its key-establishment lock before the
// potentially long human/network pairing wait.
//
// It exercises the exact stale-outer-store race: runRemotePair loads a store to
// mint the device key, then the pairing step (a) durably persists the paired host
// to a freshly loaded store — the single authoritative save — and (b) simulates
// another process durably adding a different host during the round-trip. The old
// code re-Put + Saved the stale outer snapshot after success, which rewrote the
// file from pre-pairing state and dropped the concurrent host.
func TestRunRemotePairKeepsConcurrentHostUpdate(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{DataDir: dir}

	pair := func(p config.Paths, host string, port int, profile, _, _ string) (*client.RemoteHost, error) {
		rh := &client.RemoteHost{Host: host, Port: port, Token: "tok-braw", TLSPin: "cGlu", Profile: profile}
		if err := persistRemoteHostWithoutOuterLock(t, client.RemoteHostsPath(p.DataDir), rh); err != nil {
			return nil, err
		}

		// A concurrent independent transaction adds a different host after the
		// authoritative pre-ACK save but before the command returns.
		other := &client.RemoteHost{Host: "canny.tail.ts.net", Port: 7420, Token: "tok-canny", TLSPin: "cGlu2"}
		if err := persistRemoteHostWithoutOuterLock(t, client.RemoteHostsPath(p.DataDir), other); err != nil {
			return nil, err
		}

		return rh, nil
	}

	if err := runRemotePair(paths, "ben.tail.ts.net", 7420, "", "bothy", pair, func(string, ...any) {}); err != nil {
		t.Fatalf("runRemotePair returned a spurious post-success failure: %v", err)
	}

	final, err := client.LoadRemoteHostStore(client.RemoteHostsPath(dir))
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	if _, ok := final.Get("ben.tail.ts.net"); !ok {
		t.Errorf("paired host ben.tail.ts.net missing after pairing")
	}

	if _, ok := final.Get("canny.tail.ts.net"); !ok {
		t.Errorf("concurrent host canny.tail.ts.net was clobbered by a stale outer save")
	}

	if final.DeviceKey == "" {
		t.Errorf("device key lost after pairing")
	}
}

func persistRemoteHostWithoutOuterLock(t *testing.T, path string, host *client.RemoteHost) error {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- client.PersistRemoteHost(path, host) }()

	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		return errors.New("remote-host lock remained held across pairing network wait")
	}
}
