package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
