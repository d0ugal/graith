package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/store"
)

// maxStoreResponseBytes bounds a store_get body (and, conservatively, a
// store_list payload) so the response always fits in a single control frame.
// Control frames are capped at protocol.MaxPayload (4 MiB); a response that
// exceeds the cap is silently dropped by WriteFrame, leaving the GUI's RPC
// hanging forever. We reserve headroom for the JSON envelope + escaping and
// fail with an explicit error instead.
const maxStoreResponseBytes = protocol.MaxPayload - 64*1024

// This file backs the GUI document-store browser (issue #902): read-only
// listing and fetching over the control protocol, mirroring the semantics of
// the `gr store` CLI (internal/cli/store.go) so a GUI client needs no shell or
// filesystem access. The pure resolver/listing functions live here (not inline
// in handler.go) so they can be unit-tested directly.

// storeTarget identifies a single resolved store on disk.
type storeTarget struct {
	path string
	// id is the round-trippable store identifier: "shared" for the shared store,
	// or the "<reponame>-<hash>" directory name for a repo store (as printed by
	// `gr store ls -a`). It goes back to the client so an entry can be fetched
	// without any path knowledge.
	id string
}

// resolveStoreTarget resolves a (repo, shared) request pair to a single store,
// mirroring the CLI's resolveStorePath. Repo may be a filesystem path or a
// store ID. It fails if neither repo nor shared is given, or if no matching
// store exists yet — the browser only ever addresses stores that already exist.
func resolveStoreTarget(dataDir, repo string, shared bool) (storeTarget, error) {
	// Mirror the CLI's mutual exclusion (gr store: --shared and --repo can't be
	// combined) rather than silently letting shared win — fail closed so a
	// malformed request targets nothing instead of a surprising store. A bare
	// repo == "shared" is the reserved shared-store round-trip, not a conflict.
	if shared && repo != "" && repo != "shared" {
		return storeTarget{}, errors.New("shared and repo are mutually exclusive")
	}

	if shared || repo == "shared" {
		return storeTarget{path: store.SharedStorePath(dataDir), id: "shared"}, nil
	}

	if repo == "" {
		return storeTarget{}, errors.New("store target required: set repo or shared")
	}

	// Prefer the path interpretation (existing CLI behaviour), then fall back to
	// a store-ID match so an ID from `gr store ls -a` round-trips into repo.
	if sp := store.StorePath(dataDir, config.ResolvePath(repo)); store.Exists(sp) {
		return storeTarget{path: sp, id: filepath.Base(sp)}, nil
	}

	if sp, ok := store.StorePathByID(dataDir, repo); ok {
		return storeTarget{path: sp, id: repo}, nil
	}

	return storeTarget{}, fmt.Errorf("unknown store %q: not a repo path or ID", repo)
}

// listStoreEntries builds the store_list response. When both repo and shared
// are empty it flattens every store the daemon knows about (each repo store
// plus the shared store); otherwise it lists the single resolved store. prefix,
// when non-empty, restricts the listing to keys under that path prefix.
func listStoreEntries(dataDir, repo string, shared bool, prefix string) ([]protocol.StoreEntryInfo, error) {
	if !shared && repo == "" {
		return listAllStoreEntries(dataDir, prefix)
	}

	t, err := resolveStoreTarget(dataDir, repo, shared)
	if err != nil {
		return nil, err
	}

	entries, err := store.List(t.path, prefix)
	if err != nil {
		return nil, err
	}

	infos := toStoreEntryInfos(t.id, entries)

	return infos, guardEntriesSize(infos)
}

// guardEntriesSize rejects a listing that would overflow a control frame,
// rather than letting WriteFrame silently drop it and hang the client's RPC.
func guardEntriesSize(entries []protocol.StoreEntryInfo) error {
	if n := estimateEntriesBytes(entries); n > maxStoreResponseBytes {
		return fmt.Errorf("store listing too large to return (%d entries, ~%d bytes; max %d) — narrow it with a prefix",
			len(entries), n, maxStoreResponseBytes)
	}

	return nil
}

// estimateEntriesBytes approximates the wire size of a StoreEntryInfo listing,
// so an enormous listing is rejected before it overflows a control frame.
func estimateEntriesBytes(entries []protocol.StoreEntryInfo) int {
	// ~48 bytes of JSON structure per entry (keys, quotes, RFC3339 timestamp)
	// plus the variable key + repo strings.
	total := 2
	for i := range entries {
		total += len(entries[i].Key) + len(entries[i].Repo) + 80
	}

	return total
}

// listAllStoreEntries flattens the entries of every discovered store.
// store.ListStores enumerates every "<name>" directory under store/ that holds
// a git repo, which already includes the shared store (dir name "shared").
func listAllStoreEntries(dataDir, prefix string) ([]protocol.StoreEntryInfo, error) {
	stores, err := store.ListStores(dataDir)
	if err != nil {
		return nil, err
	}

	out := []protocol.StoreEntryInfo{}

	for _, s := range stores {
		entries, err := store.List(s.Path, prefix)
		if err != nil {
			return nil, err
		}

		out = append(out, toStoreEntryInfos(s.Name, entries)...)
	}

	return out, guardEntriesSize(out)
}

// getStoreDocument fetches a single document body from the resolved store.
func getStoreDocument(dataDir, repo string, shared bool, key string) (protocol.StoreGetResponseMsg, error) {
	t, err := resolveStoreTarget(dataDir, repo, shared)
	if err != nil {
		return protocol.StoreGetResponseMsg{}, err
	}

	body, err := store.Get(t.path, key)
	if err != nil {
		// Map a missing file to a clean message — store.Get surfaces an
		// os.PathError whose text embeds the absolute on-disk path, which we
		// don't want to leak to a (possibly remote) client.
		if errors.Is(err, os.ErrNotExist) {
			return protocol.StoreGetResponseMsg{}, fmt.Errorf("document %q not found", key)
		}

		return protocol.StoreGetResponseMsg{}, err
	}

	if len(body) > maxStoreResponseBytes {
		return protocol.StoreGetResponseMsg{}, fmt.Errorf(
			"document %q is too large to fetch (%d bytes; max %d)", key, len(body), maxStoreResponseBytes)
	}

	return protocol.StoreGetResponseMsg{Key: key, Repo: t.id, Body: body}, nil
}

// toStoreEntryInfos converts store entries into their wire form, tagging each
// with its round-trippable store ID and an RFC3339 UTC timestamp. The result is
// always non-nil so the response marshals to [] rather than null.
func toStoreEntryInfos(repoID string, entries []store.Entry) []protocol.StoreEntryInfo {
	out := make([]protocol.StoreEntryInfo, len(entries))
	for i, e := range entries {
		out[i] = protocol.StoreEntryInfo{
			Key:       e.Key,
			Repo:      repoID,
			UpdatedAt: e.UpdatedAt.UTC().Format(time.RFC3339),
		}
	}

	return out
}
