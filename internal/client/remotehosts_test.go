package client

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// TestEnsureRemoteDeviceKeyConcurrentFirstKey proves that concurrent in-process
// callers contending on a brand-new store settle on ONE canonical device key
// under the cross-process lock, rather than each minting and last-writer-wins
// overwriting a different key (issue #1330).
func TestEnsureRemoteDeviceKeyConcurrentFirstKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-hosts.json")

	const workers = 8

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		pubs []string
	)

	for i := 0; i < workers; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, pub, err := EnsureRemoteDeviceKey(path)
			if err != nil {
				t.Errorf("EnsureRemoteDeviceKey: %v", err)
				return
			}

			mu.Lock()
			defer mu.Unlock()

			pubs = append(pubs, pub)
		}()
	}

	wg.Wait()

	if len(pubs) != workers {
		t.Fatalf("got %d results, want %d", len(pubs), workers)
	}

	for i, pub := range pubs {
		if pub == "" || pub != pubs[0] {
			t.Fatalf("result %d = %q, want the single canonical key %q", i, pub, pubs[0])
		}
	}

	// The persisted key must match what every worker reported.
	stored, err := LoadRemoteHostStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, storedPub, err := stored.EnsureDeviceKey(); err != nil || storedPub != pubs[0] {
		t.Fatalf("stored key = %q err=%v, workers reported %q", storedPub, err, pubs[0])
	}
}

// TestPersistRemoteHostConcurrentUpdatesMerge proves two independent host
// updates racing under the lock both survive: neither whole-file write erases
// the other, and the shared device key is untouched (issue #1330).
func TestPersistRemoteHostConcurrentUpdatesMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-hosts.json")

	_, wantPub, err := EnsureRemoteDeviceKey(path)
	if err != nil {
		t.Fatal(err)
	}

	hosts := []*RemoteHost{
		{Host: "ben.tail.ts.net", Port: 7420, Token: "tok-braw", TLSPin: "pin-braw", Profile: "bothy"},
		{Host: "canny.tail.ts.net", Port: 7421, Token: "tok-canny", TLSPin: "pin-canny", Profile: "croft"},
	}

	var wg sync.WaitGroup

	for _, h := range hosts {
		wg.Add(1)

		go func(host *RemoteHost) {
			defer wg.Done()

			if err := PersistRemoteHost(path, host); err != nil {
				t.Errorf("PersistRemoteHost(%s): %v", host.Host, err)
			}
		}(h)
	}

	wg.Wait()

	final, err := LoadRemoteHostStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, finalPub, err := final.EnsureDeviceKey(); err != nil || finalPub != wantPub {
		t.Fatalf("canonical key changed during host updates: pub=%q err=%v", finalPub, err)
	}

	for _, want := range hosts {
		got, ok := final.Get(want.Host)
		if !ok {
			t.Fatalf("host %s lost by concurrent update", want.Host)
		}

		if got.Token != want.Token || got.TLSPin != want.TLSPin || got.Profile != want.Profile {
			t.Fatalf("host %s changed: got %+v want %+v", want.Host, got, want)
		}
	}
}

// TestPersistRemoteHostRollsBackToExactPrior proves that when the durable save
// fails after the new credential has already landed on disk, PersistRemoteHost
// restores the exact prior entry, preserving the previously-working credential
// (issue #1330 rollback criterion).
func TestPersistRemoteHostRollsBackToExactPrior(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-hosts.json")

	prior := &RemoteHost{Host: "ben.tail.ts.net", Port: 7420, Token: "tok-good", TLSPin: "pin-good", Profile: "bothy"}
	if err := PersistRemoteHost(path, prior); err != nil {
		t.Fatal(err)
	}

	orig := saveRemoteHostStore

	defer func() { saveRemoteHostStore = orig }()

	calls := 0
	saveRemoteHostStore = func(s *RemoteHostStore) error {
		calls++

		if calls == 1 {
			// Let the (uncommittable) new credential land on disk, then fail so
			// rollback must restore the prior entry.
			_ = orig(s)
			return errors.New("simulated post-rename fsync failure")
		}

		return orig(s)
	}

	replacement := &RemoteHost{Host: "ben.tail.ts.net", Port: 7420, Token: "tok-bad", TLSPin: "pin-bad", Profile: "strath"}

	err := PersistRemoteHost(path, replacement)
	if err == nil {
		t.Fatal("expected a save error to be surfaced")
	}

	if !strings.Contains(err.Error(), "persist paired host") {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected the failed save then a rollback save, got %d saves", calls)
	}

	saveRemoteHostStore = orig

	stored, err := LoadRemoteHostStore(path)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := stored.Get(prior.Host)
	if !ok {
		t.Fatal("prior host destroyed by failed update")
	}

	if got.Token != prior.Token || got.TLSPin != prior.TLSPin || got.Profile != prior.Profile {
		t.Fatalf("prior credential not restored exactly: got %+v want %+v", got, prior)
	}
}

// TestRemoteHostStoreCrossProcessSerialization is the real advisory-lock
// regression for issue #1330. Independent CLI processes contend first to create
// the shared device identity, then to add two different hosts. While the parent
// holds the stable sibling lock neither subprocess may finish; after release
// both key creators must report one canonical public key and both host updates
// must survive in the latest store.
func TestRemoteHostStoreCrossProcessSerialization(t *testing.T) {
	if os.Getenv("GRAITH_TEST_REMOTE_HOST_HELPER") == "1" {
		runRemoteHostStoreHelper(t)

		return
	}

	path := filepath.Join(t.TempDir(), "remote-hosts.json")

	keys := runContendingRemoteHostHelpers(t, path, "key", "key")
	if keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("concurrent first-key results = %q and %q, want one canonical key", keys[0], keys[1])
	}

	stored, err := LoadRemoteHostStore(path)
	if err != nil {
		t.Fatal(err)
	}

	_, storedPub, err := stored.EnsureDeviceKey()
	if err != nil {
		t.Fatal(err)
	}

	if storedPub != keys[0] {
		t.Fatalf("stored public key = %q, helpers reported %q", storedPub, keys[0])
	}

	runContendingRemoteHostHelpers(t, path, "host-ben", "host-canny")

	final, err := LoadRemoteHostStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, finalPub, err := final.EnsureDeviceKey(); err != nil || finalPub != storedPub {
		t.Fatalf("canonical key changed during host updates: pub=%q err=%v", finalPub, err)
	}

	ben, benOK := final.Get("ben.tail.ts.net")
	canny, cannyOK := final.Get("canny.tail.ts.net")

	if !benOK || ben.Token != "tok-braw" || ben.TLSPin != "pin-braw" || ben.Profile != "bothy" {
		t.Fatalf("ben host missing or changed: %+v (ok=%v)", ben, benOK)
	}

	if !cannyOK || canny.Token != "tok-canny" || canny.TLSPin != "pin-canny" || canny.Profile != "croft" {
		t.Fatalf("canny host missing or changed: %+v (ok=%v)", canny, cannyOK)
	}
}

type remoteHostHelperProcess struct {
	cmd    *exec.Cmd
	ready  string
	result string
	output bytes.Buffer
}

func runContendingRemoteHostHelpers(t *testing.T, path string, actions ...string) []string {
	t.Helper()

	processes := make([]*remoteHostHelperProcess, 0, len(actions))

	err := withRemoteHostStoreLock(path, func(_ *RemoteHostStore) error {
		for i, action := range actions {
			helper := &remoteHostHelperProcess{
				ready:  filepath.Join(filepath.Dir(path), fmt.Sprintf("ready-%s-%d", action, i)),
				result: filepath.Join(filepath.Dir(path), fmt.Sprintf("result-%s-%d", action, i)),
			}
			helper.cmd = exec.Command(os.Args[0], "-test.run=^TestRemoteHostStoreCrossProcessSerialization$") //nolint:gosec // deliberately re-executes this fixed test binary.
			helper.cmd.Env = append(os.Environ(),
				"GRAITH_TEST_REMOTE_HOST_HELPER=1",
				"GRAITH_TEST_REMOTE_HOST_PATH="+path,
				"GRAITH_TEST_REMOTE_HOST_ACTION="+action,
				"GRAITH_TEST_REMOTE_HOST_READY="+helper.ready,
				"GRAITH_TEST_REMOTE_HOST_RESULT="+helper.result,
			)
			helper.cmd.Stdout = &helper.output
			helper.cmd.Stderr = &helper.output

			if err := helper.cmd.Start(); err != nil {
				return fmt.Errorf("start helper %s: %w", action, err)
			}

			processes = append(processes, helper)
		}

		deadline := time.Now().Add(5 * time.Second)

		for {
			allReady := true

			for _, helper := range processes {
				if _, err := os.Stat(helper.ready); err != nil {
					if !os.IsNotExist(err) {
						return fmt.Errorf("stat helper ready marker: %w", err)
					}

					allReady = false
				}
			}

			if allReady {
				break
			}

			if time.Now().After(deadline) {
				return errors.New("subprocesses did not reach remote-host lock")
			}

			time.Sleep(5 * time.Millisecond)
		}

		// Each helper writes its result only after its store transaction returns.
		// Give a process that has already written ready ample time to attempt the
		// lock, then prove the parent's advisory lock still excludes it.
		time.Sleep(150 * time.Millisecond)

		for _, helper := range processes {
			if _, err := os.Stat(helper.result); err == nil {
				return fmt.Errorf("helper completed while remote-host lock was held: %s", helper.result)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("stat helper result: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		for _, helper := range processes {
			_ = helper.cmd.Process.Kill()
			_ = helper.cmd.Wait()
		}

		t.Fatal(err)
	}

	results := make([]string, len(processes))

	for i, helper := range processes {
		done := make(chan error, 1)
		go func() { done <- helper.cmd.Wait() }()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("remote-host helper failed: %v\n%s", err, helper.output.String())
			}
		case <-time.After(10 * time.Second):
			_ = helper.cmd.Process.Kill()

			<-done

			t.Fatal("remote-host helper remained blocked after lock release")
		}

		data, err := os.ReadFile(helper.result)
		if err != nil {
			t.Fatalf("read helper result: %v\n%s", err, helper.output.String())
		}

		results[i] = string(data)
	}

	return results
}

func runRemoteHostStoreHelper(t *testing.T) {
	t.Helper()

	ready := os.Getenv("GRAITH_TEST_REMOTE_HOST_READY")
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil { //nolint:gosec // G703: helper path is supplied by this test's parent process.
		t.Fatal(err)
	}

	path := os.Getenv("GRAITH_TEST_REMOTE_HOST_PATH")
	result := ""

	switch action := os.Getenv("GRAITH_TEST_REMOTE_HOST_ACTION"); action {
	case "key":
		_, pub, err := EnsureRemoteDeviceKey(path)
		if err != nil {
			t.Fatal(err)
		}

		result = pub
	case "host-ben":
		err := PersistRemoteHost(path, &RemoteHost{Host: "ben.tail.ts.net", Port: 7420, Token: "tok-braw", TLSPin: "pin-braw", Profile: "bothy"})
		if err != nil {
			t.Fatal(err)
		}

		result = "ok"
	case "host-canny":
		err := PersistRemoteHost(path, &RemoteHost{Host: "canny.tail.ts.net", Port: 7421, Token: "tok-canny", TLSPin: "pin-canny", Profile: "croft"})
		if err != nil {
			t.Fatal(err)
		}

		result = "ok"
	default:
		t.Fatalf("unknown remote-host helper action %q", action)
	}

	if err := os.WriteFile(os.Getenv("GRAITH_TEST_REMOTE_HOST_RESULT"), []byte(result), 0o600); err != nil { //nolint:gosec // G703: helper path is supplied by this test's parent process.
		t.Fatal(err)
	}
}
