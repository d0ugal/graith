package daemon

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/store"
)

// seedStore initialises a store at storePath and writes the given key→body
// documents, so the browse helpers have something to enumerate.
func seedStore(t *testing.T, storePath string, docs map[string]string) {
	t.Helper()

	if err := store.Init(storePath); err != nil {
		t.Fatalf("init store %s: %v", storePath, err)
	}

	for key, body := range docs {
		if err := store.Put(storePath, key, body); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
}

func TestResolveStoreTarget(t *testing.T) {
	dataDir := t.TempDir()

	repoRoot := "/glen/croft"
	repoStore := store.StorePath(dataDir, repoRoot)
	seedStore(t, repoStore, map[string]string{"loch/notes.md": "still waters"})
	seedStore(t, store.SharedStorePath(dataDir), map[string]string{"blether.md": "a wee chat"})

	repoID := store.StorePath(dataDir, repoRoot)[len(dataDir)+len("/store/"):]

	t.Run("shared flag", func(t *testing.T) {
		got, err := resolveStoreTarget(dataDir, "", true)
		if err != nil {
			t.Fatal(err)
		}

		if got.id != "shared" || got.path != store.SharedStorePath(dataDir) {
			t.Fatalf("unexpected shared target: %+v", got)
		}
	})

	t.Run("shared by repo name", func(t *testing.T) {
		got, err := resolveStoreTarget(dataDir, "shared", false)
		if err != nil {
			t.Fatal(err)
		}

		if got.id != "shared" {
			t.Fatalf("expected shared, got %q", got.id)
		}
	})

	t.Run("repo path", func(t *testing.T) {
		got, err := resolveStoreTarget(dataDir, repoRoot, false)
		if err != nil {
			t.Fatal(err)
		}

		if got.id != repoID || got.path != repoStore {
			t.Fatalf("unexpected repo target: %+v (want id %q)", got, repoID)
		}
	})

	t.Run("repo id round-trips", func(t *testing.T) {
		got, err := resolveStoreTarget(dataDir, repoID, false)
		if err != nil {
			t.Fatal(err)
		}

		if got.path != repoStore {
			t.Fatalf("id %q did not resolve to %q: %+v", repoID, repoStore, got)
		}
	})

	t.Run("no target is an error", func(t *testing.T) {
		if _, err := resolveStoreTarget(dataDir, "", false); err == nil {
			t.Fatal("expected error for empty repo without shared")
		}
	})

	t.Run("unknown store is an error", func(t *testing.T) {
		if _, err := resolveStoreTarget(dataDir, "thrawn-deadbeef0000", false); err == nil {
			t.Fatal("expected error for unknown store id")
		}
	})

	t.Run("shared and repo are mutually exclusive", func(t *testing.T) {
		if _, err := resolveStoreTarget(dataDir, repoRoot, true); err == nil {
			t.Fatal("expected error when both shared and a repo are given")
		}

		// shared + the reserved "shared" repo value is not a conflict.
		if _, err := resolveStoreTarget(dataDir, "shared", true); err != nil {
			t.Fatalf("shared + repo=shared should resolve, got %v", err)
		}
	})
}

func TestBrowseLayerRejectsTraversal(t *testing.T) {
	dataDir := t.TempDir()
	repoRoot := "/glen/croft"
	seedStore(t, store.StorePath(dataDir, repoRoot), map[string]string{"loch/notes.md": "still waters"})

	// Malicious keys/prefixes must be rejected at the browse layer (via the
	// store package's ValidateKey), before any filesystem access. This locks the
	// security boundary at the layer this feature introduces.
	badKeys := []string{"../../etc/passwd", ".git/config", "..", "/etc/hosts"}
	for _, key := range badKeys {
		if _, err := getStoreDocument(dataDir, repoRoot, false, key); err == nil {
			t.Errorf("getStoreDocument accepted traversal key %q", key)
		}
	}

	for _, prefix := range []string{"../../etc", ".git"} {
		if _, err := listStoreEntries(dataDir, repoRoot, false, prefix); err == nil {
			t.Errorf("listStoreEntries accepted traversal prefix %q", prefix)
		}
	}
}

func TestGuardEntriesSize(t *testing.T) {
	// A modest listing is fine.
	small := make([]protocol.StoreEntryInfo, 100)
	for i := range small {
		small[i] = protocol.StoreEntryInfo{Key: "loch/notes.md", Repo: "croft-abc", UpdatedAt: "x"}
	}

	if err := guardEntriesSize(small); err != nil {
		t.Fatalf("small listing rejected: %v", err)
	}

	// A listing that would overflow a control frame is rejected, not silently
	// dropped by WriteFrame (which would hang the client).
	huge := make([]protocol.StoreEntryInfo, maxStoreResponseBytes/80+10)
	for i := range huge {
		huge[i] = protocol.StoreEntryInfo{Key: "k", Repo: "r", UpdatedAt: "x"}
	}

	if err := guardEntriesSize(huge); err == nil {
		t.Fatal("expected an oversized listing to be rejected")
	}
}

func TestGetStoreDocumentTooLarge(t *testing.T) {
	dataDir := t.TempDir()
	repoRoot := "/glen/croft"
	big := strings.Repeat("x", maxStoreResponseBytes+1)
	seedStore(t, store.StorePath(dataDir, repoRoot), map[string]string{"loch/big.txt": big})

	if _, err := getStoreDocument(dataDir, repoRoot, false, "loch/big.txt"); err == nil {
		t.Fatal("expected an oversized document to be rejected")
	}
}

func TestListStoreEntries(t *testing.T) {
	dataDir := t.TempDir()

	repoRoot := "/glen/croft"
	repoStore := store.StorePath(dataDir, repoRoot)
	seedStore(t, repoStore, map[string]string{
		"loch/notes.md": "still waters",
		"loch/plans.md": "a bonnie scheme",
		"brae/hill.txt": "up the slope",
	})
	seedStore(t, store.SharedStorePath(dataDir), map[string]string{"blether.md": "a wee chat"})

	t.Run("single repo store", func(t *testing.T) {
		entries, err := listStoreEntries(dataDir, repoRoot, false, "")
		if err != nil {
			t.Fatal(err)
		}

		if len(entries) != 3 {
			t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
		}

		for _, e := range entries {
			if e.Repo == "shared" {
				t.Fatalf("repo store entry mislabelled shared: %+v", e)
			}

			if e.UpdatedAt == "" {
				t.Fatalf("entry missing timestamp: %+v", e)
			}
		}
	})

	t.Run("prefix filter", func(t *testing.T) {
		entries, err := listStoreEntries(dataDir, repoRoot, false, "loch")
		if err != nil {
			t.Fatal(err)
		}

		if len(entries) != 2 {
			t.Fatalf("expected 2 entries under loch/, got %d: %+v", len(entries), entries)
		}
	})

	t.Run("shared store", func(t *testing.T) {
		entries, err := listStoreEntries(dataDir, "", true, "")
		if err != nil {
			t.Fatal(err)
		}

		if len(entries) != 1 || entries[0].Repo != "shared" || entries[0].Key != "blether.md" {
			t.Fatalf("unexpected shared entries: %+v", entries)
		}
	})

	t.Run("all stores flattened", func(t *testing.T) {
		entries, err := listStoreEntries(dataDir, "", false, "")
		if err != nil {
			t.Fatal(err)
		}

		// 3 repo docs + 1 shared doc.
		if len(entries) != 4 {
			t.Fatalf("expected 4 entries across all stores, got %d: %+v", len(entries), entries)
		}

		var sawShared, sawRepo bool

		for _, e := range entries {
			if e.Repo == "shared" {
				sawShared = true
			} else {
				sawRepo = true
			}
		}

		if !sawShared || !sawRepo {
			t.Fatalf("expected entries from both shared and repo stores: %+v", entries)
		}
	})

	t.Run("empty when no stores", func(t *testing.T) {
		entries, err := listStoreEntries(t.TempDir(), "", false, "")
		if err != nil {
			t.Fatal(err)
		}

		if entries == nil {
			t.Fatal("expected non-nil (empty) slice, got nil")
		}

		if len(entries) != 0 {
			t.Fatalf("expected no entries, got %+v", entries)
		}
	})
}

func TestGetStoreDocument(t *testing.T) {
	dataDir := t.TempDir()

	repoRoot := "/glen/croft"
	repoStore := store.StorePath(dataDir, repoRoot)
	seedStore(t, repoStore, map[string]string{"loch/notes.md": "still waters"})

	t.Run("fetches body", func(t *testing.T) {
		resp, err := getStoreDocument(dataDir, repoRoot, false, "loch/notes.md")
		if err != nil {
			t.Fatal(err)
		}

		if resp.Body != "still waters" || resp.Key != "loch/notes.md" {
			t.Fatalf("unexpected response: %+v", resp)
		}

		if resp.Repo == "shared" || resp.Repo == "" {
			t.Fatalf("expected a repo-store id, got %q", resp.Repo)
		}
	})

	t.Run("missing key is an error", func(t *testing.T) {
		if _, err := getStoreDocument(dataDir, repoRoot, false, "haar/missing.md"); err == nil {
			t.Fatal("expected error for missing document")
		}
	})

	t.Run("no target is an error", func(t *testing.T) {
		if _, err := getStoreDocument(dataDir, "", false, "loch/notes.md"); err == nil {
			t.Fatal("expected error when no store target is given")
		}
	})
}

// --- handler wiring -------------------------------------------------------

func TestStoreListHandler(t *testing.T) {
	h := newTestHarness(t)

	repoStore := store.StorePath(h.sm.paths.DataDir, "/glen/croft")
	seedStore(t, repoStore, map[string]string{"loch/notes.md": "still waters"})

	h.sendControl(t, "store_list", protocol.StoreListMsg{})

	env := h.expectType(t, "store_list")

	var resp protocol.StoreListResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Entries) != 1 || resp.Entries[0].Key != "loch/notes.md" {
		t.Fatalf("unexpected store_list response: %+v", resp.Entries)
	}
}

func TestStoreGetHandler(t *testing.T) {
	h := newTestHarness(t)

	repoStore := store.StorePath(h.sm.paths.DataDir, "/glen/croft")
	seedStore(t, repoStore, map[string]string{"loch/notes.md": "still waters"})

	repoID := repoStore[len(h.sm.paths.DataDir)+len("/store/"):]

	h.sendControl(t, "store_get", protocol.StoreGetMsg{Repo: repoID, Key: "loch/notes.md"})

	env := h.expectType(t, "store_get")

	var resp protocol.StoreGetResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Body != "still waters" {
		t.Fatalf("unexpected store_get body: %q", resp.Body)
	}
}

func TestStoreListHandlerRepoAndPrefix(t *testing.T) {
	h := newTestHarness(t)

	repoStore := store.StorePath(h.sm.paths.DataDir, "/glen/croft")
	seedStore(t, repoStore, map[string]string{
		"loch/notes.md": "still waters",
		"brae/hill.txt": "up the slope",
	})

	repoID := repoStore[len(h.sm.paths.DataDir)+len("/store/"):]

	h.sendControl(t, "store_list", protocol.StoreListMsg{Repo: repoID, Prefix: "loch"})

	env := h.expectType(t, "store_list")

	var resp protocol.StoreListResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Entries) != 1 || resp.Entries[0].Key != "loch/notes.md" {
		t.Fatalf("expected only the loch/ entry, got %+v", resp.Entries)
	}
}

func TestStoreListHandlerRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-sess", "thrawn", "tok-thrawn")

	// A session token must not reach store browsing — that would let an agent
	// read across the sandbox's per-repo store isolation via the daemon.
	h.sendControlWithToken(t, "store_list", protocol.StoreListMsg{}, "tok-thrawn")

	h.expectError(t, "human operator")
}

func TestStoreGetHandlerRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "fash-sess", "fash", "tok-fash")

	h.sendControlWithToken(t, "store_get",
		protocol.StoreGetMsg{Repo: "croft-abc", Key: "loch/notes.md"}, "tok-fash")

	h.expectError(t, "human operator")
}

func TestStoreGetHandlerUnknownStore(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "store_get", protocol.StoreGetMsg{Repo: "thrawn-deadbeef0000", Key: "haar.md"})

	h.expectError(t, "unknown store")
}

func TestStoreListHandlerInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	// A non-object payload fails DecodePayload.
	h.sendControl(t, "store_list", []string{"not", "an", "object"})

	h.expectError(t, "invalid store_list message")
}
