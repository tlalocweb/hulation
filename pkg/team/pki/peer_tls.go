package pki

// Peer-dial TLS helpers. Stage 3 nodes dial each other by
// raft.ServerAddress (typically host:port). Each node's cert has a
// SAN scoped to its own node id (foo.team.internal,
// <team>/foo.team.internal), so the stdlib hostname check would
// reject any cross-node dial that uses a fixed ServerName. Instead
// we present *.team.internal as the SNI (which fires the listener's
// internal-channel gate), and verify the peer's chain against the
// Team CA pool ourselves — node identity isn't tied to hostname,
// it's tied to having a Team-CA-signed cert.

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// PeerSNI is the SNI ServerName Stage 3 dialers should present
// when reaching out to another team node. It's a syntactic trigger
// for the listener's internal-channel gate (HA_PLAN3 §4.1) — the
// real authentication is mTLS chain verification.
const PeerSNI = "peer" + SANInternalSuffix

// PeerDialTLSConfig builds the *tls.Config that every internal-
// channel dialer (Raft transport, StorageProxy, Relay, Gossip,
// ChatLookup) uses. The chain check happens in VerifyPeerCertificate
// so we can keep the SNI loose — node certs are unique per node and
// would never match a fixed ServerName.
//
// Bundle is the loaded per-node mTLS material (LoadBundle). pool is
// the Team CA root pool that signed both ends.
func PeerDialTLSConfig(b *Bundle) (*tls.Config, error) {
	if b == nil {
		return nil, errors.New("bundle is required")
	}
	leaf, err := tls.X509KeyPair(b.Cert, b.Key)
	if err != nil {
		return nil, fmt.Errorf("parse node cert+key: %w", err)
	}
	pool := b.CAPool

	cfg := &tls.Config{
		Certificates:       []tls.Certificate{leaf},
		ServerName:         PeerSNI,
		RootCAs:            pool,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("no peer cert")
			}
			peerCert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("parse peer cert: %w", err)
			}
			opts := x509.VerifyOptions{
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}
			for _, b := range rawCerts[1:] {
				inter, perr := x509.ParseCertificate(b)
				if perr != nil {
					return perr
				}
				if opts.Intermediates == nil {
					opts.Intermediates = x509.NewCertPool()
				}
				opts.Intermediates.AddCert(inter)
			}
			_, err = peerCert.Verify(opts)
			if err != nil {
				return fmt.Errorf("peer chain: %w", err)
			}
			return nil
		},
	}
	return cfg, nil
}
