package raftbackend

// streamConn turns a gRPC bidirectional stream into a net.Conn.
// hashicorp/raft's NetworkTransport sees a byte-oriented duplex
// connection; we hide the message-oriented framing of gRPC behind
// it. Each Send/Recv carries a RaftFrame whose payload is opaque
// bytes.

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	hraft "github.com/hashicorp/raft"
	"google.golang.org/grpc"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
)

// streamConn is shared by both ends. Two constructors below
// specialise initialisation for client vs server sides.
type streamConn struct {
	send   func([]byte) error
	recv   func() ([]byte, error)
	closeF func() error

	local  net.Addr
	remote net.Addr

	mu      sync.Mutex
	leftover []byte
	rdDeadline time.Time
	wrDeadline time.Time
	closed     bool

	done   chan struct{}
}

func newServerStreamConn(stream internalspec.RaftTransportService_StreamServer, local hraft.ServerAddress) *streamConn {
	c := &streamConn{
		local: raftStreamAddr(local),
		done:  make(chan struct{}),
	}
	c.remote = raftStreamAddr(remotePeerAddr(stream.Context()))

	c.send = func(b []byte) error {
		return stream.Send(&internalspec.RaftFrame{
			Kind:    internalspec.RaftFrame_KIND_DATA,
			Payload: b,
		})
	}
	c.recv = func() ([]byte, error) {
		f, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return f.GetPayload(), nil
	}
	c.closeF = func() error {
		c.markDone()
		return nil
	}
	return c
}

func newClientStreamConn(
	stream internalspec.RaftTransportService_StreamClient,
	gconn *grpc.ClientConn,
	cancel context.CancelFunc,
	target, local hraft.ServerAddress,
) *streamConn {
	c := &streamConn{
		local:  raftStreamAddr(local),
		remote: raftStreamAddr(target),
		done:   make(chan struct{}),
	}
	c.send = func(b []byte) error {
		return stream.Send(&internalspec.RaftFrame{
			Kind:    internalspec.RaftFrame_KIND_DATA,
			Payload: b,
		})
	}
	c.recv = func() ([]byte, error) {
		f, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return f.GetPayload(), nil
	}
	c.closeF = func() error {
		_ = stream.CloseSend()
		cancel()
		return gconn.Close()
	}
	return c
}

func (c *streamConn) markDone() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	close(c.done)
}

func (c *streamConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, io.EOF
	}
	if len(c.leftover) > 0 {
		n := copy(p, c.leftover)
		c.leftover = c.leftover[n:]
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()

	buf, err := c.recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			c.markDone()
		}
		return 0, err
	}
	n := copy(p, buf)
	if n < len(buf) {
		c.mu.Lock()
		c.leftover = append(c.leftover, buf[n:]...)
		c.mu.Unlock()
	}
	return n, nil
}

func (c *streamConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, errors.New("write on closed stream")
	}
	c.mu.Unlock()
	if err := c.send(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *streamConn) Close() error {
	c.markDone()
	if c.closeF != nil {
		return c.closeF()
	}
	return nil
}

func (c *streamConn) LocalAddr() net.Addr  { return c.local }
func (c *streamConn) RemoteAddr() net.Addr { return c.remote }

// SetDeadline / SetReadDeadline / SetWriteDeadline are best-effort.
// gRPC streams don't expose a per-direction deadline; we record the
// values for future expansion (the caller in hashicorp/raft only
// uses these informationally for its connection-pool TTL — actual
// deadlines come from the per-RPC Context).
func (c *streamConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdDeadline = t
	c.wrDeadline = t
	c.mu.Unlock()
	return nil
}
func (c *streamConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdDeadline = t
	c.mu.Unlock()
	return nil
}
func (c *streamConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.wrDeadline = t
	c.mu.Unlock()
	return nil
}

// remotePeerAddr extracts the dialer's reported address from a gRPC
// server context for log line clarity. It isn't load-bearing for
// correctness — Raft's identity check uses the LocalID set on the
// peer side, not this address.
func remotePeerAddr(ctx context.Context) hraft.ServerAddress {
	type peerCtxKey struct{}
	if v := ctx.Value(peerCtxKey{}); v != nil {
		if s, ok := v.(string); ok {
			return hraft.ServerAddress(s)
		}
	}
	return hraft.ServerAddress("grpc-peer")
}
