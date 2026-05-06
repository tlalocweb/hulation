package raftbackend

// gRPC-backed Raft StreamLayer for HA Stage 3. Tunnels each Raft RPC
// (AppendEntries / RequestVote / InstallSnapshot / TimeoutNow) over
// a single bidirectional streaming gRPC call so Raft traffic shares
// the unified HTTPS port with everything else (HA_PLAN3 §4.2).
//
// hashicorp/raft's NetworkTransport already handles framing,
// msgpack, and the request/response choreography on top of any
// io.Reader/io.Writer pair. We supply the lowest layer — a
// StreamLayer (= net.Listener + Dialer) — backed by gRPC streams.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	hraft "github.com/hashicorp/raft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
)

// peerAddrConverter resolves a hashicorp/raft.ServerAddress (the
// raft_addr a node was registered with) into the dial target the
// gRPC client should use. Solo deployments and node-local tests can
// pass an identity converter.
type peerAddrConverter func(hraft.ServerAddress) string

// GRPCStreamLayer is the raft.StreamLayer + RaftTransportService
// server impl. One instance is created per node, registered on the
// internal gRPC server, and handed to NewNetworkTransport.
type GRPCStreamLayer struct {
	internalspec.UnimplementedRaftTransportServiceServer

	advertise  hraft.ServerAddress
	creds      credentials.TransportCredentials
	addrFor    peerAddrConverter
	dialOpts   []grpc.DialOption

	mu      sync.Mutex
	closed  bool
	accepts chan net.Conn
}

// GRPCStreamLayerConfig parameters the new transport.
type GRPCStreamLayerConfig struct {
	// AdvertiseAddr is the raft_addr we report as our LocalAddr —
	// callers (raft state machine) compare this when picking which
	// RPC to forward where.
	AdvertiseAddr string
	// Credentials carries the mTLS material for outbound dials.
	// Server-side (incoming) uses the unified listener's TLS config;
	// this field is only for the dialing side.
	Credentials credentials.TransportCredentials
	// AddrFor optionally rewrites a raft.ServerAddress for the dial
	// target (e.g. add a port suffix). nil → identity.
	AddrFor peerAddrConverter
	// AcceptBacklog caps inbound stream queue depth. Default 16.
	AcceptBacklog int
	// DialOptions are merged with our defaults on every Dial.
	DialOptions []grpc.DialOption
}

// NewGRPCStreamLayer builds an unstarted layer. Caller must register
// it on the internal gRPC server with
// internalspec.RegisterRaftTransportServiceServer(server, layer).
func NewGRPCStreamLayer(c GRPCStreamLayerConfig) (*GRPCStreamLayer, error) {
	if c.AdvertiseAddr == "" {
		return nil, errors.New("raft.transport_grpc: AdvertiseAddr required")
	}
	if c.Credentials == nil {
		return nil, errors.New("raft.transport_grpc: Credentials required")
	}
	if c.AcceptBacklog <= 0 {
		c.AcceptBacklog = 16
	}
	conv := c.AddrFor
	if conv == nil {
		conv = func(s hraft.ServerAddress) string { return string(s) }
	}
	return &GRPCStreamLayer{
		advertise: hraft.ServerAddress(c.AdvertiseAddr),
		creds:     c.Credentials,
		addrFor:   conv,
		dialOpts:  c.DialOptions,
		accepts:   make(chan net.Conn, c.AcceptBacklog),
	}, nil
}

// Stream is the bidirectional RPC every peer dials. Each new client
// stream becomes one net.Conn passed up to the raft state machine
// via Accept().
func (l *GRPCStreamLayer) Stream(stream internalspec.RaftTransportService_StreamServer) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return status.Error(codes.Unavailable, "raft transport closed")
	}
	l.mu.Unlock()

	conn := newServerStreamConn(stream, l.advertise)
	select {
	case l.accepts <- conn:
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
	<-conn.done
	return nil
}

// Accept implements net.Listener.
func (l *GRPCStreamLayer) Accept() (net.Conn, error) {
	c, ok := <-l.accepts
	if !ok {
		return nil, io.EOF
	}
	return c, nil
}

// Close implements net.Listener.
func (l *GRPCStreamLayer) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	close(l.accepts)
	return nil
}

// Addr implements net.Listener — returns a synthetic address that
// hashicorp/raft only uses for log lines.
func (l *GRPCStreamLayer) Addr() net.Addr {
	return raftStreamAddr(l.advertise)
}

// Dial opens a fresh gRPC client stream to a peer and wraps it as
// a net.Conn. Called by NetworkTransport when it needs to send a
// new RPC.
func (l *GRPCStreamLayer) Dial(target hraft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return nil, errors.New("raft transport closed")
	}

	addr := l.addrFor(target)
	dialCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(l.creds),
		grpc.WithBlock(),
	}, l.dialOpts...)

	conn, err := grpc.DialContext(dialCtx, addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("raft.transport_grpc: dial %s: %w", addr, err)
	}

	client := internalspec.NewRaftTransportServiceClient(conn)
	streamCtx, streamCancel := context.WithCancel(context.Background())
	stream, err := client.Stream(streamCtx)
	if err != nil {
		streamCancel()
		_ = conn.Close()
		return nil, fmt.Errorf("raft.transport_grpc: open stream %s: %w", addr, err)
	}
	return newClientStreamConn(stream, conn, streamCancel, target, l.advertise), nil
}

// raftStreamAddr satisfies net.Addr with a Raft-flavoured network
// name so log lines stay self-describing.
type raftStreamAddr hraft.ServerAddress

func (a raftStreamAddr) Network() string { return "raft-grpc" }
func (a raftStreamAddr) String() string  { return string(a) }
