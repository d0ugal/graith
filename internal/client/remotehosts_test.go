package client

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteHostStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-hosts.json")

	s, err := LoadRemoteHostStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Hosts) != 0 {
		t.Fatalf("fresh store has %d hosts, want 0", len(s.Hosts))
	}

	s.Put(&RemoteHost{Host: "graith-ben.ts.net", Port: 4823, Token: "braw-token", TLSPin: "pin==", Profile: ""})

	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadRemoteHostStore(path)
	if err != nil {
		t.Fatal(err)
	}

	h, ok := reloaded.Get("graith-ben.ts.net")
	if !ok || h.Port != 4823 || h.Token != "braw-token" || h.TLSPin != "pin==" {
		t.Errorf("host not round-tripped: %+v (ok=%v)", h, ok)
	}
}

func TestRemoteHostStoreLoadMissing(t *testing.T) {
	s, err := LoadRemoteHostStore(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("loading a missing store should not error: %v", err)
	}

	if s.Hosts == nil {
		t.Error("missing store should have an initialized Hosts map")
	}
}

func TestEnsureDeviceKeyStableAndSigns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-hosts.json")
	s, _ := LoadRemoteHostStore(path)

	priv1, pub1, err := s.EnsureDeviceKey()
	if err != nil {
		t.Fatal(err)
	}

	if pub1 == "" || len(priv1) != ed25519.PrivateKeySize {
		t.Fatal("EnsureDeviceKey returned an invalid key")
	}

	// Stable within the same store.
	_, pub1b, _ := s.EnsureDeviceKey()
	if pub1b != pub1 {
		t.Error("EnsureDeviceKey should be stable within a store")
	}

	// Persists across save/reload.
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, _ := LoadRemoteHostStore(path)

	priv2, pub2, err := reloaded.EnsureDeviceKey()
	if err != nil {
		t.Fatal(err)
	}

	if pub2 != pub1 {
		t.Error("device key should persist across reload")
	}

	// The key actually signs + verifies (proof-of-possession primitive).
	nonce := []byte("haar-nonce")
	sig := ed25519.Sign(priv2, nonce)

	if !ed25519.Verify(priv2.Public().(ed25519.PublicKey), nonce, sig) {
		t.Error("device key failed to sign/verify")
	}
}

func TestRemoteHostStoreResolve(t *testing.T) {
	s, _ := LoadRemoteHostStore(filepath.Join(t.TempDir(), "remote-hosts.json"))
	s.Put(&RemoteHost{Host: "ben.tail-glen.ts.net", Port: 4823})
	s.Put(&RemoteHost{Host: "brae.tail-glen.ts.net", Port: 4823})
	s.Put(&RemoteHost{Host: "whin.tail-a.ts.net", Port: 4823})
	s.Put(&RemoteHost{Host: "whin.tail-b.ts.net", Port: 4823})

	// Exact key.
	if h, cand := s.Resolve("ben.tail-glen.ts.net"); h == nil || cand != nil {
		t.Errorf("exact match: h=%+v cand=%v", h, cand)
	}

	// Unique short-name (only one host starts with "ben.").
	if h, _ := s.Resolve("ben"); h == nil || h.Host != "ben.tail-glen.ts.net" {
		t.Errorf("short-name match: %+v", h)
	}

	// A trailing dot in the query is tolerated.
	if h, _ := s.Resolve("brae."); h == nil || h.Host != "brae.tail-glen.ts.net" {
		t.Errorf("trailing-dot match: %+v", h)
	}

	// Ambiguous short-name (two "whin.*" hosts) does not resolve.
	if h, cand := s.Resolve("whin"); h != nil || len(cand) != 4 {
		t.Errorf("ambiguous short-name should not resolve: h=%+v cand=%v", h, cand)
	}

	// No match returns nil + the sorted candidate list.
	h, cand := s.Resolve("dreich")
	if h != nil {
		t.Errorf("expected no match for dreich, got %+v", h)
	}

	if len(cand) != 4 || cand[0] != "ben.tail-glen.ts.net" {
		t.Errorf("expected 4 sorted candidates, got %v", cand)
	}
}

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

	// atomicfile surfaces the directory-creation failure; the exact wording is
	// "create dir" but any dir error is acceptable here.
	if !strings.Contains(err.Error(), "dir") {
		t.Errorf("expected a directory error, got %v", err)
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
