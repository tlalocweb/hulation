package pki

// Peer-dial TLS helpers. Stage 3 nodes dial each other by
// raft.ServerAddress (typically host:port). Every per-node cert is
// issued with the fixed PeerSNI ("peer.team.internal") in its DNS
// SAN list (alongside the per-node and per-team-per-node names), so
// the stdlib hostname check passes when a dialer presents PeerSNI
// as the ServerName. Chain verification against the Team CA pool
// is what authenticates the peer; PeerSNI is just the syntactic
// trigger that fires the listener's internal-channel gate
// (HA_PLAN3 §4.1).

import (
	"crypto/tls"
	"errors"
	"fmt"
)

// PeerSNI is the SNI ServerName Stage 3 dialers present when
// reaching out to another team node. The unified listener uses it
// to route to the internal mTLS gRPC server; per-node certs carry
// it in their SAN list so stdlib hostname verification passes.
const PeerSNI = "peer" + SANInternalSuffix

// PeerDialTLSConfig builds the *tls.Config that every internal-
// channel dialer (Raft transport, StorageProxy, Relay, Gossip,
// ChatLookup) uses. The stdlib does the chain + hostname check
// against PeerSNI; only Team-CA-signed certs whose SANs include
// PeerSNI will validate.
//
// Bundle is the loaded per-node mTLS material (LoadBundle).
func PeerDialTLSConfig(b *Bundle) (*tls.Config, error) {
	if b == nil {
		return nil, errors.New("bundle is required")
	}
	leaf, err := tls.X509KeyPair(b.Cert, b.Key)
	if err != nil {
		return nil, fmt.Errorf("parse node cert+key: %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{leaf},
		ServerName:   PeerSNI,
		RootCAs:      b.CAPool,
		MinVersion:   tls.VersionTLS12,
	}
	return cfg, nil
}
