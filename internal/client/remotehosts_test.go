package client

import (
	"crypto/ed25519"
	"path/filepath"
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
