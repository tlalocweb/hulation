package gossip

// PeerSet abstracts "who are my Team peers right now". Production
// reads this from raftbackend.RaftStorage.Members(); tests pass a
// static slice. Decoupled here so the gossip layer doesn't import
// the raft backend (which would be a cycle: raft → relay → gossip
// → raft).

import (
	"context"
	"sync"
)

// Peer is the dial-target shape the gossip protocol consumes.
type Peer struct {
	NodeID   string
	RaftAddr string // host:443 — the unified listener of that peer
}

// PeerProvider returns the current peer set, MINUS the local node.
// The gossip layer calls this on every push + every anti-entropy
// tick. Implementations must be cheap.
type PeerProvider interface {
	Peers(ctx context.Context) ([]Peer, error)
}

// StaticPeers is a no-op PeerProvider for tests.
type StaticPeers struct {
	mu    sync.Mutex
	peers []Peer
}

// NewStaticPeers wraps a slice for test use.
func NewStaticPeers(peers []Peer) *StaticPeers {
	return &StaticPeers{peers: append([]Peer(nil), peers...)}
}

// Peers implements PeerProvider.
func (s *StaticPeers) Peers(_ context.Context) ([]Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Peer(nil), s.peers...), nil
}

// SetPeers replaces the slice (test-time only).
func (s *StaticPeers) SetPeers(peers []Peer) {
	s.mu.Lock()
	s.peers = append([]Peer(nil), peers...)
	s.mu.Unlock()
}
