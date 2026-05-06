package raftbackend_test

// Integration test for HA Stage 3.5: a follower-side Put / Mutate /
// Batch must transparently forward to the leader via the
// LeaderForwarder hook (in production: StorageProxyService.Apply
// over the internal mTLS gRPC channel). The test wires an in-process
// forwarder that calls applyEncodedAsLeader on the leader directly,
// proving the encode/forward/apply contract without spinning up two
// hula instances.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
	pkipkg "github.com/tlalocweb/hulation/pkg/team/pki"
)

// TestForwardToLeader_RoundTripsThroughGRPC builds a 2-node cluster
// with the gRPC stream layer, wires the StorageProxyService on both
// sides, then writes through the follower and reads from the leader.
func TestForwardToLeader_RoundTripsThroughGRPC(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-node Raft bootstrap takes >5s")
	}

	// Two-node setup: both share a CA so they trust each other's
	// peer certs. Each gets its own listener + StreamLayer + RaftStorage.
	ca, err := pkipkg.GenerateCA("forward-test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)

	leaderNode := startNode(t, ca, pool, "leader", true /* bootstrap */)
	followerNode := startNode(t, ca, pool, "follower", false)

	// Leader waits for leadership.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := leaderNode.rs.WaitLeader(ctx); err != nil {
		t.Fatalf("leader WaitLeader: %v", err)
	}

	// AddVoter the follower. (In production this is the Membership
	// flow — for the test we go direct.)
	if err := leaderNode.rs.AddVoter(followerNode.id, followerNode.addr, 0, 5*time.Second); err != nil {
		t.Fatalf("AddVoter: %v", err)
	}
	// Give Raft a moment to propagate cluster config.
	waitFollower(t, followerNode.rs, 10*time.Second)

	// Sanity: follower should see leader.
	if followerNode.rs.IsLeader() {
		t.Fatal("follower unexpectedly became leader before forwarder wired")
	}

	// Wire the follower → leader forwarder. Forwarder dials the
	// leader's gRPC StorageProxyService and ships the encoded
	// command bytes verbatim. SNI is the leader's *.team.internal SAN.
	dialTLS, err := pkipkg.PeerDialTLSConfig(&pkipkg.Bundle{
		CACertPEM: ca.CertPEM,
		CAPool:    pool,
		Cert:      followerNode.leafPEM,
		Key:       followerNode.keyPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials.NewTLS(dialTLS)
	followerNode.rs.SetLeaderForwarder(func(ctx context.Context, _, leaderAddr string, encoded []byte) error {
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn, err := grpc.DialContext(dialCtx, leaderAddr,
			grpc.WithTransportCredentials(creds),
			grpc.WithBlock(),
		)
		if err != nil {
			return err
		}
		defer conn.Close()
		_, err = internalspec.NewStorageProxyServiceClient(conn).Apply(ctx,
			&internalspec.ApplyRequest{Command: encoded})
		return err
	})

	// Write through the follower. The forwarder ships the bytes; the
	// leader applies them; FSM emits the value to both nodes.
	if err := followerNode.rs.Put(ctx, "goals/forwarded-key", []byte("from-follower")); err != nil {
		t.Fatalf("follower Put: %v", err)
	}

	// Read on the leader.
	got, err := leaderNode.rs.Get(ctx, "goals/forwarded-key")
	if err != nil {
		t.Fatalf("leader Get: %v", err)
	}
	if string(got) != "from-follower" {
		t.Errorf("leader Get returned %q, want %q", got, "from-follower")
	}

	// Read on the follower. Raft fan-out → FSM apply on the
	// follower happens after our forwarded Put returns from the
	// leader; poll briefly so the test isn't flaky on slow CI.
	deadline := time.Now().Add(5 * time.Second)
	var got2 []byte
	for time.Now().Before(deadline) {
		got2, err = followerNode.rs.Get(ctx, "goals/forwarded-key")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("follower Get (final): %v", err)
	}
	if string(got2) != "from-follower" {
		t.Errorf("follower Get returned %q, want %q", got2, "from-follower")
	}

	// Cleanup: the leader's gRPC server has the StorageProxy + a
	// reference to the leader's RaftStorage; closing in reverse
	// order matters.
	followerNode.cleanup()
	leaderNode.cleanup()
}

// --- test helpers --------------------------------------------------

type testNode struct {
	id      string
	addr    string
	rs      *raftbackend.RaftStorage
	leafPEM []byte
	keyPEM  []byte
	cleanup func()
}

func startNode(t *testing.T, ca *pkipkg.CA, pool *x509.CertPool, id string, bootstrap bool) *testNode {
	t.Helper()

	leaf, err := pkipkg.GenerateNodeCert(ca, "forward-test", id, id+".team.internal", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	srvTLS := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	// Dial creds: chain-only verification so any peer's cert
	// validates without needing the SAN to match a fixed ServerName.
	cliTLS, err := pkipkg.PeerDialTLSConfig(&pkipkg.Bundle{
		CACertPEM: ca.CertPEM,
		CAPool:    pool,
		Cert:      leaf.CertPEM,
		Key:       leaf.KeyPEM,
	})
	if err != nil {
		t.Fatal(err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()

	stream, sErr := raftbackend.NewGRPCStreamLayer(raftbackend.GRPCStreamLayerConfig{
		AdvertiseAddr: addr,
		Credentials:   credentials.NewTLS(cliTLS),
	})
	if sErr != nil {
		t.Fatal(sErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	rs, err := raftbackend.New(raftbackend.Config{
		NodeID:        id,
		DataDir:       t.TempDir(),
		AdvertiseAddr: addr,
		Bootstrap:     bootstrap,
		StreamLayer:   stream,
	})
	if err != nil {
		t.Fatalf("raftbackend.New(%s): %v", id, err)
	}

	// Build the gRPC server, register every service backed by rs,
	// THEN Serve — gRPC disallows post-Serve registration.
	gsrv := grpc.NewServer(grpc.Creds(credentials.NewTLS(srvTLS)))
	internalspec.RegisterRaftTransportServiceServer(gsrv, stream)
	internalspec.RegisterStorageProxyServiceServer(gsrv, &localProxyServer{rs: rs})
	go func() { _ = gsrv.Serve(lis) }()

	return &testNode{
		id:      id,
		addr:    addr,
		rs:      rs,
		leafPEM: leaf.CertPEM,
		keyPEM:  leaf.KeyPEM,
		cleanup: func() {
			_ = rs.Close()
			gsrv.Stop()
			_ = lis.Close()
		},
	}
}

func waitFollower(t *testing.T, rs *raftbackend.RaftStorage, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !rs.IsLeader() {
			_, addr := rs.LeaderInfo()
			if addr != "" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("follower did not learn the leader within %s", timeout)
}

// localProxyServer is the test-side StorageProxyService handler.
// Production uses pkg/api/v1/storageproxy; here we want zero
// circular deps in the raftbackend test package, so we inline a
// minimal one.
type localProxyServer struct {
	internalspec.UnimplementedStorageProxyServiceServer
	rs *raftbackend.RaftStorage
}

func (l *localProxyServer) Apply(ctx context.Context, req *internalspec.ApplyRequest) (*internalspec.ApplyResponse, error) {
	idx, err := l.rs.ApplyEncodedAsLeader(ctx, req.GetCommand())
	if err != nil {
		if errors.Is(err, raftbackend.ErrNotLeader) {
			return nil, errors.New("not leader")
		}
		return nil, err
	}
	return &internalspec.ApplyResponse{AppliedIndex: idx}, nil
}

// Compile-time assertion: *raftbackend.RaftStorage still satisfies
// storage.Storage post the 3.5 surface additions.
var _ storagepkg.Storage = (*raftbackend.RaftStorage)(nil)
