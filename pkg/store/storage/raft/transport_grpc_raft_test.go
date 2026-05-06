package raftbackend_test

// Integration test: single-node Raft cluster booted with the gRPC
// StreamLayer instead of TCP, proving the new transport survives
// the bootstrap + apply path.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	hraft "github.com/hashicorp/raft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
	pkipkg "github.com/tlalocweb/hulation/pkg/team/pki"
)

func TestRaftStorage_GRPCTransport_BootCommitRead(t *testing.T) {
	// 1. PKI: self-signed CA + a single node cert serves both the
	//    listener side and the dialing side (loopback test).
	ca, err := pkipkg.GenerateCA("test-team", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := pkipkg.GenerateNodeCert(ca, "test-team", "node-solo", "node-solo.team.internal", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)

	srvTLS := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	cliTLS := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   "node-solo.team.internal",
	}

	// 2. Listener + gRPC server hosting the StreamLayer.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	stream, err := raftbackend.NewGRPCStreamLayer(raftbackend.GRPCStreamLayerConfig{
		AdvertiseAddr: lis.Addr().String(),
		Credentials:   credentials.NewTLS(cliTLS),
		AddrFor: func(_ hraft.ServerAddress) string {
			return lis.Addr().String()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gsrv := grpc.NewServer(grpc.Creds(credentials.NewTLS(srvTLS)))
	internalspec.RegisterRaftTransportServiceServer(gsrv, stream)
	go func() { _ = gsrv.Serve(lis) }()
	t.Cleanup(gsrv.Stop)

	// 3. Boot Raft with the gRPC stream layer instead of TCP.
	rs, err := raftbackend.New(raftbackend.Config{
		NodeID:        "node-solo",
		DataDir:       t.TempDir(),
		AdvertiseAddr: lis.Addr().String(),
		Bootstrap:     true,
		StreamLayer:   stream,
	})
	if err != nil {
		t.Fatalf("raftbackend.New: %v", err)
	}
	t.Cleanup(func() { _ = rs.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rs.WaitLeader(ctx); err != nil {
		t.Fatalf("WaitLeader: %v", err)
	}

	// 4. Commit a write through Raft, read it back. If the gRPC
	//    transport is mis-framing bytes, this would surface as
	//    a CRC failure or apply timeout.
	// goals/<id> routes through the known bucket router so we
	// don't ride the unrouted-fallback path during the test.
	if err := rs.Put(ctx, "goals/transport-grpc-test", []byte("world")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := rs.Get(ctx, "goals/transport-grpc-test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "world" {
		t.Errorf("Get returned %q, want %q", got, "world")
	}
	// Ensure the storage is still a real Storage and not nil-typed.
	if _, ok := storage.Storage(rs).(*raftbackend.RaftStorage); !ok {
		t.Error("type assertion back to *RaftStorage failed — interface satisfaction broke")
	}
}
