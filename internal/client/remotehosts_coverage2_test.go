package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteHostsPath2(t *testing.T) {
	got := RemoteHostsPath("/data/glen")
	want := filepath.Join("/data/glen", "remote-hosts.json")

	if got != want {
		t.Errorf("RemoteHostsPath = %q, want %q", got, want)
	}
}

func TestLoadRemoteHostStoreRejectsGarbage2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-hosts.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadRemoteHostStore(path); err == nil {
		t.Fatal("loading a corrupt store should error")
	}
}

func TestLoadRemoteHostStoreNilHostsInitialized2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-hosts.json")
	// Valid JSON with no "hosts" key — the loader must still hand back a usable
	// (non-nil) map so Put doesn't panic.
	if err := os.WriteFile(path, []byte(`{"device_key":""}`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := LoadRemoteHostStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if s.Hosts == nil {
		t.Fatal("Hosts map should be initialized after loading a store without it")
	}

	s.Put(&RemoteHost{Host: "braw.ts.net"})

	if _, ok := s.Get("braw.ts.net"); !ok {
		t.Error("Put/Get failed on a store loaded without a hosts key")
	}
}

func TestEnsureDeviceKeyRejectsCorruptKey2(t *testing.T) {
	s := &RemoteHostStore{Hosts: map[string]*RemoteHost{}, DeviceKey: "!!!not-base64!!!"}

	if _, _, err := s.EnsureDeviceKey(); err == nil {
		t.Fatal("a non-base64 device key should be rejected as corrupt")
	}

	// A valid base64 string of the wrong length is also corrupt.
	s.DeviceKey = "YWJj" // "abc" — too short for an ed25519 private key
	if _, _, err := s.EnsureDeviceKey(); err == nil {
		t.Fatal("a wrong-length device key should be rejected as corrupt")
	}
}

func TestSaveEnforces0600AndCreatesDir2(t *testing.T) {
	// Nested path exercises the MkdirAll branch.
	path := filepath.Join(t.TempDir(), "nested", "dir", "remote-hosts.json")
	s := &RemoteHostStore{Hosts: map[string]*RemoteHost{}, path: path}

	priv, _, err := s.EnsureDeviceKey()
	if err != nil {
		t.Fatal(err)
	}

	_ = priv

	if err := s.Save(); err != nil {
		t.Fatalf("Save should create parent dirs and write the store: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store perms = %o, want 0600", perm)
	}

	// The temp file must not linger after a successful rename.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file should be gone after a successful Save")
	}
}

func TestSaveErrorsWhenDirIsAFile2(t *testing.T) {
	// Point the store at a path whose parent is a regular file, so MkdirAll
	// fails and Save surfaces the error instead of silently succeeding.
	base := t.TempDir()

	fileAsDir := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &RemoteHostStore{
		Hosts: map[string]*RemoteHost{},
		path:  filepath.Join(fileAsDir, "sub", "remote-hosts.json"),
	}

	err := s.Save()
	if err == nil {
		t.Fatal("Save should error when the parent path is a file")
	}

	if !strings.Contains(err.Error(), "data dir") {
		t.Errorf("expected a data-dir error, got %v", err)
	}
}

func TestNamesSorted2(t *testing.T) {
	s := &RemoteHostStore{Hosts: map[string]*RemoteHost{}}
	s.Put(&RemoteHost{Host: "whin.ts.net"})
	s.Put(&RemoteHost{Host: "braw.ts.net"})
	s.Put(&RemoteHost{Host: "canny.ts.net"})

	names := s.Names()
	if len(names) != 3 || names[0] != "braw.ts.net" || names[2] != "whin.ts.net" {
		t.Errorf("Names not sorted: %v", names)
	}
}
