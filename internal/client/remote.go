package client

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// spkiPinVerifier returns a TLS peer-certificate verifier that accepts the
// connection only if the leaf certificate's SubjectPublicKeyInfo SHA-256
// (base64) equals the pinned value. This matches the daemon's computeSPKIPin,
// so the pin survives certificate renewal (same key → same pin).
func spkiPinVerifier(pin string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("no server certificate presented")
		}

		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parse server cert: %w", err)
		}

		der, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
		if err != nil {
			return fmt.Errorf("marshal server public key: %w", err)
		}

		sum := sha256.Sum256(der)
		got := base64.StdEncoding.EncodeToString(sum[:])

		if subtle.ConstantTimeCompare([]byte(got), []byte(pin)) != 1 {
			return fmt.Errorf("TLS SPKI pin mismatch (possible MITM); expected %s got %s", pin, got)
		}

		return nil
	}
}

// remoteTLSConfig builds the client TLS config: standard hostname validation is
// disabled in favour of SPKI pinning (self-signed daemon certs won't chain).
func remoteTLSConfig(rh *RemoteHost) *tls.Config {
	return &tls.Config{
		ServerName:            rh.Host,
		InsecureSkipVerify:    true, //nolint:gosec // G402: chain validation replaced by SPKI pin below
		VerifyPeerCertificate: spkiPinVerifier(rh.TLSPin),
		MinVersion:            tls.VersionTLS12,
		// Disable resumption: VerifyPeerCertificate is skipped on resumed
		// sessions, which would bypass the SPKI pin (gosec G123).
		SessionTicketsDisabled: true,
	}
}

// ConnectRemote dials a paired remote daemon over TLS (SPKI-pinned), performs
// the handshake, completes proof-of-possession (signing the daemon's challenge
// with the device key), and returns a ready Client authenticated as this
// device. Unlike New/Connect it never touches the local daemon (no
// EnsureDaemon, no auto-upgrade).
func ConnectRemote(paths config.Paths, rh *RemoteHost, signer ed25519.PrivateKey, cols, rows uint16) (*Client, error) {
	raw, err := net.DialTimeout("tcp", net.JoinHostPort(rh.Host, strconv.Itoa(rh.Port)), remoteDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", rh.Host, err)
	}

	conn := tls.Client(raw, remoteTLSConfig(rh))

	// Bound the TLS handshake itself: a TCP peer that accepts the connection but
	// never completes the TLS handshake would otherwise hang conn.Handshake()
	// indefinitely (issue #1242). Install the deadline BEFORE the handshake, not
	// after. Re-armed below for the app handshake + proof-of-possession, and
	// cleared before the long-lived passthrough loop.
	_ = conn.SetDeadline(time.Now().Add(remoteHandshakeTimeout))

	if err := conn.Handshake(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tls handshake with %s: %w", rh.Host, err)
	}

	// Bound the handshake + proof-of-possession exchange so an unresponsive
	// daemon can't hang the client forever. Cleared before the long-lived
	// passthrough loop below.
	_ = conn.SetDeadline(time.Now().Add(remoteHandshakeTimeout))

	c := &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
		paths:  paths,
		token:  rh.Token,
	}

	// Handshake — the profile must match the remote daemon's.
	hs := BuildHandshake(paths, cols, rows, "")
	hs.Profile = rh.Profile

	if err := c.SendControl("handshake", hs); err != nil {
		c.Close()
		return nil, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		c.Close()
		return nil, err
	}

	switch resp.Type {
	case "handshake_ok":
	case "handshake_err":
		var e protocol.HandshakeErrMsg

		_ = protocol.DecodePayload(resp, &e)

		c.Close()

		return nil, fmt.Errorf("handshake rejected: %s", e.Reason)
	default:
		c.Close()
		return nil, fmt.Errorf("unexpected handshake response: %s", resp.Type)
	}

	if err := c.completeRemotePoP(signer, rh.TLSPin); err != nil {
		c.Close()
		return nil, err
	}

	// Clear the handshake deadline — the attach/passthrough that follows is
	// long-lived.
	_ = conn.SetDeadline(time.Time{})

	return c, nil
}

// PairRemote performs the CLI side of device pairing with a remote daemon: it
// dials (capturing the server's SPKI pin via TOFU), handshakes, sends
// pair_request with the device public key, and blocks until the remote human
// runs `gr pair approve`, then returns the minted RemoteHost credentials. No
// token or proof-of-possession is used — this is the roleNone pairing lane.
func PairRemote(paths config.Paths, host string, port int, profile, deviceLabel, devicePubKey string) (*RemoteHost, error) {
	raw, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), remoteDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", host, err)
	}

	// TOFU: accept whatever cert the daemon presents and capture its SPKI pin;
	// we confirm it against the pin the daemon reports in pair_response.
	var capturedPin string

	tlsConf := &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true, //nolint:gosec // G402: first-contact TOFU; pin captured + confirmed below
		MinVersion:         tls.VersionTLS12,
		// Disable resumption: VerifyPeerCertificate is skipped on resumed
		// sessions, which would bypass pin capture (gosec G123).
		SessionTicketsDisabled: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("no server certificate")
			}

			cert, perr := x509.ParseCertificate(rawCerts[0])
			if perr != nil {
				return perr
			}

			der, merr := x509.MarshalPKIXPublicKey(cert.PublicKey)
			if merr != nil {
				return merr
			}

			sum := sha256.Sum256(der)
			capturedPin = base64.StdEncoding.EncodeToString(sum[:])

			return nil
		},
	}

	conn := tls.Client(raw, tlsConf)

	// Bound the TLS handshake itself (first-contact pairing lane): a peer that
	// accepts TCP but never completes TLS must not hang conn.Handshake(). Install
	// the deadline BEFORE the handshake, not after (issue #1242). Re-armed below
	// for the app handshake, then extended to the pairing wait.
	_ = conn.SetDeadline(time.Now().Add(remoteHandshakeTimeout))

	if err := conn.Handshake(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}

	// Bound the handshake so an unresponsive daemon can't hang forever.
	_ = conn.SetDeadline(time.Now().Add(remoteHandshakeTimeout))

	c := &Client{conn: conn, reader: protocol.NewFrameReader(conn), writer: protocol.NewFrameWriter(conn), paths: paths}
	defer c.Close()

	hs := BuildHandshake(paths, fallbackCols, fallbackRows, "")
	hs.Profile = profile

	if err := c.SendControl("handshake", hs); err != nil {
		return nil, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, err
	}

	if resp.Type == "handshake_err" {
		var e protocol.HandshakeErrMsg

		_ = protocol.DecodePayload(resp, &e)

		return nil, fmt.Errorf("handshake rejected: %s (try --profile if the daemon runs a named profile)", e.Reason)
	}

	if resp.Type != "handshake_ok" {
		return nil, fmt.Errorf("unexpected handshake response: %s", resp.Type)
	}

	// Awaiting local human approval can legitimately take minutes; extend the
	// deadline to just past the daemon's pending-pairing TTL.
	_ = conn.SetDeadline(time.Now().Add(remotePairingTimeout))

	return c.completePairing(host, port, deviceLabel, devicePubKey, capturedPin)
}

// completePairing runs the receipt-protocol pairing exchange (issue #1299) on an
// already-handshaked connection: pair_request (advertising receipt capability) →
// pair_response (validate the daemon's TLS pin) → durably store the credential →
// pair_ack → pair_committed (validate it refers to this exact request/device).
// capturedPin is the SPKI pin observed on the TLS handshake.
//
// The credential is stored BEFORE the ack, so a crash between the ack and the
// caller's later save cannot leave the daemon paired with no client credential.
// It therefore returns without error only on a confirmed commit, but on a
// post-ack failure it deliberately RETAINS the stored credential (the daemon may
// already be durable) and surfaces the error — commit-unknown, not rolled back.
func (c *Client) completePairing(host string, port int, deviceLabel, devicePubKey, capturedPin string) (*RemoteHost, error) {
	// The daemon issues an auth_challenge to every remote connection; as an
	// unpaired device we ignore it and proceed on the pairing lane. ReceiptAck
	// advertises that this client will acknowledge pair_response with pair_ack and
	// wait for pair_committed; the daemon rejects a pair_request without it.
	if err := c.SendControl("pair_request", protocol.PairRequestMsg{DeviceLabel: deviceLabel, DevicePubKey: devicePubKey, ReceiptAck: true}); err != nil {
		return nil, err
	}

	// pending holds the validated credentials between pair_response (delivery) and
	// pair_committed (durable commit).
	var (
		pending          *RemoteHost
		pendingRequestID string
		pendingDeviceID  string
		stored           bool
	)

	// commitUnknown wraps any failure that happens AFTER the credential was stored
	// and the ack was (attempted to be) sent. At that point no outcome proves the
	// daemon did not commit, so the on-disk credential is deliberately retained and
	// the outcome is surfaced as commit-status-unknown — the user must not assume a
	// rollback and blindly re-pair (issue #1299).
	commitUnknown := func(err error) error {
		return fmt.Errorf("pairing commit status unknown (credential retained; do not assume it rolled back): %w", err)
	}

	// Block until the pairing completes. The sequence is: auth_challenge (ignored)
	// → pair_response → durably store the credential → pair_ack → pair_committed.
	for {
		env, rerr := c.ReadControlResponse()
		if rerr != nil {
			if stored {
				return nil, commitUnknown(rerr)
			}

			return nil, rerr
		}

		switch env.Type {
		case "auth_challenge":
			continue // pairing lane ignores PoP
		case "pair_response":
			// A second pair_response after we've already staged one is anomalous —
			// refuse rather than re-acking or overwriting a pending credential.
			if pending != nil {
				return nil, errors.New("daemon sent a second pair_response")
			}

			var pr protocol.PairResponseMsg
			if derr := protocol.DecodePayload(env, &pr); derr != nil {
				return nil, derr
			}

			// device + token are always required; request_id is absent from a
			// legacy (pre-receipt) daemon's response (handled below).
			if pr.DeviceID == "" || pr.ClientToken == "" {
				return nil, errors.New("daemon sent an incomplete pair_response (missing device/token)")
			}

			// Require the daemon to report its pin and confirm it matches the
			// cert we were served — refuse the weaker "accept whatever was
			// presented" path so pairing always confirms the endpoint.
			if pr.TLSPinSPKI == "" {
				return nil, errors.New("daemon reported no TLS pin; refusing to pair (cannot confirm the endpoint)")
			}

			if pr.TLSPinSPKI != capturedPin {
				return nil, fmt.Errorf("TLS pin mismatch: daemon reported %s but presented %s (possible MITM)", pr.TLSPinSPKI, capturedPin)
			}

			host := &RemoteHost{Host: host, Port: port, Token: pr.ClientToken, TLSPin: capturedPin, Profile: pr.DaemonProfile}

			// Cross-version: a legacy (pre-receipt) daemon ignores receipt_ack,
			// already committed the device durably during `gr pair approve`, and omits
			// request_id. It understands neither pair_ack nor pair_committed — so
			// store the credential and complete WITHOUT acking, rather than discarding
			// the one-time token and stranding the device (issue #1299).
			if pr.RequestID == "" {
				if err := c.persistPairedHost(host); err != nil {
					return nil, fmt.Errorf("persist paired host (legacy daemon): %w", err)
				}

				return host, nil
			}

			pending = host
			pendingRequestID = pr.RequestID
			pendingDeviceID = pr.DeviceID

			// Durably store the credential BEFORE acking, so a crash or disconnect
			// between pair_ack and the caller's later Save cannot leave the daemon
			// paired with no client-side credential (issue #1299). The caller's
			// subsequent Put/Save of the same host is harmless.
			if err := c.persistPairedHost(pending); err != nil {
				return nil, fmt.Errorf("persist paired host before acknowledging receipt: %w", err)
			}

			stored = true

			// Acknowledge receipt so the daemon durably commits the device. The send
			// itself is post-store: even a send error is ambiguous (bytes may have
			// reached the daemon), so it is commit-unknown, not a rollback.
			if err := c.SendControl("pair_ack", protocol.PairAckMsg{RequestID: pr.RequestID, DeviceID: pr.DeviceID}); err != nil {
				return nil, commitUnknown(fmt.Errorf("acknowledge pairing receipt: %w", err))
			}
		case "pair_committed":
			if pending == nil {
				return nil, errors.New("daemon reported pairing committed before delivering credentials")
			}

			var pc protocol.PairCommittedMsg
			if derr := protocol.DecodePayload(env, &pc); derr != nil {
				return nil, commitUnknown(fmt.Errorf("malformed pair_committed: %w", derr))
			}

			// Confirm the commit refers to the credential we actually received, so a
			// stale or mismatched commit frame can't complete the pairing. The
			// already-stored credential is deliberately retained (the daemon may have
			// committed the real device) — surface it as commit-unknown.
			if pc.RequestID != pendingRequestID || pc.DeviceID != pendingDeviceID {
				return nil, commitUnknown(fmt.Errorf("pair_committed mismatch: got request %q device %q, expected request %q device %q",
					pc.RequestID, pc.DeviceID, pendingRequestID, pendingDeviceID))
			}

			return pending, nil
		case "error":
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(env, &e)

			// A daemon error AFTER we stored + acked is ambiguous (it may be a
			// post-rename state-save failure that already landed the device on disk),
			// so it is commit-unknown/retain; before store it is an ordinary failure.
			if stored {
				return nil, commitUnknown(fmt.Errorf("daemon error after ack: %s", e.Message))
			}

			return nil, fmt.Errorf("pairing failed: %s", e.Message)
		default:
			if stored {
				return nil, commitUnknown(fmt.Errorf("unexpected reply after ack: %s", env.Type))
			}

			return nil, fmt.Errorf("unexpected reply during pairing: %s", env.Type)
		}
	}
}

// persistPairedHost durably writes the paired host to the client's remote-hosts
// store before the receipt is acknowledged (issue #1299). It is transactional: a
// failed write (e.g. a post-rename dir-fsync error that already landed the new,
// not-yet-committable token on disk) must not destroy a previously-working
// credential — so it snapshots the exact prior entry and, on any Save error,
// durably restores it (surfacing a combined error if the restore also fails).
func (c *Client) persistPairedHost(rh *RemoteHost) error {
	store, err := LoadRemoteHostStore(RemoteHostsPath(c.paths.DataDir))
	if err != nil {
		return err
	}

	// Snapshot the exact prior entry by value, so restoring it can't be disturbed
	// by the Put below.
	prior, hadPrior := store.Get(rh.Host)

	var priorCopy RemoteHost
	if hadPrior {
		priorCopy = *prior
	}

	store.Put(rh)

	if saveErr := store.Save(); saveErr != nil {
		if hadPrior {
			store.Put(&priorCopy)
		} else {
			delete(store.Hosts, rh.Host)
		}

		if rbErr := store.Save(); rbErr != nil {
			return fmt.Errorf("persist paired host before ack: %w; rollback also failed: %w", saveErr, rbErr)
		}

		return fmt.Errorf("persist paired host before ack: %w", saveErr)
	}

	return nil
}

// completeRemotePoP answers the daemon's auth_challenge with a signed auth_proof
// and consumes the auth_ok acknowledgement, so the connection advances to its
// paired role before any RPC is issued. The signature binds the nonce to the
// pinned server SPKI (spki) so a MITM cannot relay the proof (issue #886).
func (c *Client) completeRemotePoP(signer ed25519.PrivateKey, spki string) error {
	chEnv, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	if chEnv.Type != "auth_challenge" {
		return fmt.Errorf("expected auth_challenge, got %s", chEnv.Type)
	}

	var ch protocol.AuthChallengeMsg
	if err := protocol.DecodePayload(chEnv, &ch); err != nil {
		return err
	}

	// Refuse to sign an unbound (relayable) proof. In practice the pinned TLS
	// handshake already failed closed if the pin were empty, but make the
	// channel-binding invariant explicit here too (mirrors the Swift client).
	if spki == "" {
		return errors.New("cannot complete proof-of-possession without a pinned TLS channel to bind against")
	}

	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(signer, protocol.PoPSigningInput(ch.Nonce, spki)))

	if err := c.SendControl("auth_proof", protocol.AuthProofMsg{Signature: sig}); err != nil {
		return err
	}

	ack, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	switch ack.Type {
	case "auth_ok":
		return nil
	case "error":
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(ack, &e)

		return fmt.Errorf("proof of possession rejected: %s", e.Message)
	default:
		return fmt.Errorf("expected auth_ok, got %s", ack.Type)
	}
}
