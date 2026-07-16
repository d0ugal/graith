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
	raw, err := net.DialTimeout("tcp", net.JoinHostPort(rh.Host, strconv.Itoa(rh.Port)), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", rh.Host, err)
	}

	conn := tls.Client(raw, remoteTLSConfig(rh))
	if err := conn.Handshake(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tls handshake with %s: %w", rh.Host, err)
	}

	// Bound the handshake + proof-of-possession exchange so an unresponsive
	// daemon can't hang the client forever. Cleared before the long-lived
	// passthrough loop below.
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

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
	raw, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 10*time.Second)
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
	if err := conn.Handshake(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}

	// Bound the handshake so an unresponsive daemon can't hang forever.
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	c := &Client{conn: conn, reader: protocol.NewFrameReader(conn), writer: protocol.NewFrameWriter(conn), paths: paths}
	defer c.Close()

	hs := BuildHandshake(paths, 80, 24, "")
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

	// The daemon issues an auth_challenge to every remote connection; as an
	// unpaired device we ignore it and proceed on the pairing lane.
	if err := c.SendControl("pair_request", protocol.PairRequestMsg{DeviceLabel: deviceLabel, DevicePubKey: devicePubKey}); err != nil {
		return nil, err
	}

	// Awaiting local human approval can legitimately take minutes; extend the
	// deadline to just past the daemon's pending-pairing TTL.
	_ = conn.SetDeadline(time.Now().Add(11 * time.Minute))

	// Block until approved. Skip the auth_challenge that arrives first.
	for {
		env, rerr := c.ReadControlResponse()
		if rerr != nil {
			return nil, rerr
		}

		switch env.Type {
		case "auth_challenge":
			continue // pairing lane ignores PoP
		case "pair_response":
			var pr protocol.PairResponseMsg
			if derr := protocol.DecodePayload(env, &pr); derr != nil {
				return nil, derr
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

			return &RemoteHost{Host: host, Port: port, Token: pr.ClientToken, TLSPin: capturedPin, Profile: pr.DaemonProfile}, nil
		case "error":
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(env, &e)

			return nil, fmt.Errorf("pairing failed: %s", e.Message)
		default:
			return nil, fmt.Errorf("unexpected reply during pairing: %s", env.Type)
		}
	}
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
