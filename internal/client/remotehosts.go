package client

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/d0ugal/graith/internal/atomicfile"
)

// RemoteHost is a paired remote graith daemon reachable over the tailnet
// (design §A/§B, #615). Credentials are minted by the remote daemon's `gr pair
// approve` and delivered in pair_response.
type RemoteHost struct {
	Host    string `json:"host"`           // MagicDNS name / tailnet address
	Port    int    `json:"port"`           // remote listener port
	Token   string `json:"client_token"`   // bearer token (this device's)
	TLSPin  string `json:"tls_pin_spki"`   // pinned SPKI (TOFU)
	Profile string `json:"daemon_profile"` // remote daemon profile (handshake must match)
}

// RemoteHostStore persists this CLI's device identity and its paired remote
// hosts. It lives in the data dir (0600) — the local filesystem is the trust
// boundary, mirroring the daemon's 0700 socket.
type RemoteHostStore struct {
	// DeviceKey is this device's ed25519 private key (base64), used for
	// proof-of-possession against every paired remote daemon.
	DeviceKey string                 `json:"device_key"`
	Hosts     map[string]*RemoteHost `json:"hosts"`
	path      string
}

// RemoteHostsPath returns the store path within a data dir.
func RemoteHostsPath(dataDir string) string {
	return filepath.Join(dataDir, "remote-hosts.json")
}

// remoteHostsLockPath is a stable sibling of remote-hosts.json. The data file
// itself is atomically replaced on every save, so locking that inode would let a
// second process open the replacement and bypass the first process's lock.
func remoteHostsLockPath(path string) string {
	return path + ".lock"
}

// withRemoteHostStoreLock serializes one load/mutate/save transaction across
// CLI processes. The callback receives a store loaded only after the advisory
// lock is acquired, so it always merges against the latest published host map
// and canonical device identity. Callers must keep human and network waits
// outside the callback.
func withRemoteHostStoreLock(path string, fn func(*RemoteHostStore) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { //nolint:gosec // G703: path is the config-managed graith data file.
		return fmt.Errorf("create remote-hosts directory: %w", err)
	}

	lockFile, err := os.OpenFile(remoteHostsLockPath(path), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G703: path is the config-managed graith data file.
	if err != nil {
		return fmt.Errorf("open remote-hosts lock: %w", err)
	}
	defer func() { _ = lockFile.Close() }()

	for {
		err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}

	if err != nil {
		return fmt.Errorf("acquire remote-hosts lock: %w", err)
	}

	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	store, err := LoadRemoteHostStore(path)
	if err != nil {
		return err
	}

	return fn(store)
}

// LoadRemoteHostStore loads the store, returning an empty one if the file does
// not exist.
func LoadRemoteHostStore(path string) (*RemoteHostStore, error) {
	s := &RemoteHostStore{Hosts: map[string]*RemoteHost{}, path: path}

	data, err := os.ReadFile(path) //nolint:gosec // G703: callers supply the config-managed graith data file.
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}

		return nil, fmt.Errorf("read remote-hosts: %w", err)
	}

	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse remote-hosts: %w", err)
	}

	if s.Hosts == nil {
		s.Hosts = map[string]*RemoteHost{}
	}

	s.path = path

	return s, nil
}

// Save writes the store atomically (temp file + rename) with 0600 perms, so a
// crash mid-write can't corrupt the credential store or lose the device key,
// and an existing file with looser perms is replaced by a 0600 one.
// remoteHostsWrite is the durable write primitive Save uses. It is a package var
// so tests can inject a post-rename failure (data already on disk, call errors).
var remoteHostsWrite = atomicfile.Write

func (s *RemoteHostStore) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	// Use the crash-safe primitive: temp write + fsync + atomic rename + dir
	// fsync. The receipt protocol treats a successful Save as the durable
	// pre-pair_ack barrier (issue #1299), so an unsynced rename is not enough —
	// a crash after rename but before the entry is flushed could otherwise lose
	// the credential the daemon is about to commit against.
	if err := remoteHostsWrite(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write remote hosts: %w", err)
	}

	return nil
}

// EnsureRemoteDeviceKey establishes or reloads the one canonical device key
// while holding the cross-process store lock, then durably republishes the
// latest store before returning it. The lock is released before the caller
// begins the potentially long human/network pairing wait.
func EnsureRemoteDeviceKey(path string) (ed25519.PrivateKey, string, error) {
	var (
		priv ed25519.PrivateKey
		pub  string
	)

	err := withRemoteHostStoreLock(path, func(store *RemoteHostStore) error {
		var err error

		priv, pub, err = store.EnsureDeviceKey()
		if err != nil {
			return err
		}

		return store.Save()
	})
	if err != nil {
		return nil, "", err
	}

	return priv, pub, nil
}

// PersistRemoteHost reloads and durably updates one paired host while holding
// the cross-process store lock. On any save error it restores the exact prior
// entry before releasing the lock; this includes post-rename errors where the
// new, not-yet-acknowledged credential may already be visible on disk. Because
// the transaction starts from the latest store, unrelated hosts and the
// canonical device key survive both the update and rollback.
func PersistRemoteHost(path string, host *RemoteHost) error {
	if host == nil {
		return errors.New("persist remote host: nil host")
	}

	return withRemoteHostStoreLock(path, func(store *RemoteHostStore) error {
		prior, hadPrior := store.Get(host.Host)

		var priorCopy *RemoteHost

		if prior != nil {
			priorValue := *prior
			priorCopy = &priorValue
		}

		store.Put(host)

		if saveErr := store.Save(); saveErr != nil {
			if hadPrior {
				store.Hosts[host.Host] = priorCopy
			} else {
				delete(store.Hosts, host.Host)
			}

			if rollbackErr := store.Save(); rollbackErr != nil {
				return fmt.Errorf("persist paired host before ack: %w; rollback also failed: %w", saveErr, rollbackErr)
			}

			return fmt.Errorf("persist paired host before ack: %w", saveErr)
		}

		return nil
	})
}

// EnsureDeviceKey returns this device's ed25519 private key and base64 public
// key, generating and storing the key on first use. The caller should Save
// after a fresh key is generated.
func (s *RemoteHostStore) EnsureDeviceKey() (ed25519.PrivateKey, string, error) {
	if s.DeviceKey != "" {
		raw, err := base64.StdEncoding.DecodeString(s.DeviceKey)
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, "", errors.New("corrupt device key in remote-hosts store")
		}

		priv := ed25519.PrivateKey(raw)
		pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))

		return priv, pub, nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generate device key: %w", err)
	}

	s.DeviceKey = base64.StdEncoding.EncodeToString(priv)

	return priv, base64.StdEncoding.EncodeToString(pub), nil
}

// Get returns a paired host by name.
func (s *RemoteHostStore) Get(host string) (*RemoteHost, bool) {
	h, ok := s.Hosts[host]
	return h, ok
}

// Names returns the paired host keys, sorted — for stable listings and error
// messages.
func (s *RemoteHostStore) Names() []string {
	names := make([]string, 0, len(s.Hosts))
	for name := range s.Hosts {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// Resolve finds a paired host by exact key or by short-name/prefix match, so a
// user can type "myhost" for a host stored as "myhost.tailnet.ts.net". On a
// unique match it returns the host; otherwise it returns nil plus the sorted
// list of paired names (empty, or the candidates) for a helpful error.
func (s *RemoteHostStore) Resolve(host string) (*RemoteHost, []string) {
	if h, ok := s.Hosts[host]; ok {
		return h, nil
	}

	var matches []*RemoteHost

	for name, h := range s.Hosts {
		// "myhost" matches "myhost.tailnet.ts.net" (label boundary), and a
		// trailing-dot query is tolerated.
		if strings.HasPrefix(name, strings.TrimSuffix(host, ".")+".") {
			matches = append(matches, h)
		}
	}

	if len(matches) == 1 {
		return matches[0], nil
	}

	return nil, s.Names()
}

// Put stores/updates a paired host (keyed by Host).
func (s *RemoteHostStore) Put(h *RemoteHost) {
	s.Hosts[h.Host] = h
}
