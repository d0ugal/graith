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

// LoadRemoteHostStore loads the store, returning an empty one if the file does
// not exist.
func LoadRemoteHostStore(path string) (*RemoteHostStore, error) {
	s := &RemoteHostStore{Hosts: map[string]*RemoteHost{}, path: path}

	data, err := os.ReadFile(path)
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
func (s *RemoteHostStore) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}

	// os.WriteFile only applies the mode on create; enforce 0600 explicitly in
	// case the temp file pre-existed with looser perms.
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	return nil
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
