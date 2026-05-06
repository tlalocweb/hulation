package raftbackend

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	hraft "github.com/hashicorp/raft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	pkipkg "github.com/tlalocweb/hulation/pkg/team/pki"
)

// TestGRPCStreamLayer_RoundTrip stands up a real gRPC server on a
// loopback port, registers the transport, and dials it from a client
// transport. Bytes written into the client side must come out on the
// server side and vice versa.
func TestGRPCStreamLayer_RoundTrip(t *testing.T) {
	ca, err := pkipkg.GenerateCA("test-team", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srvLeaf, err := pkipkg.GenerateNodeCert(ca, "test-team", "node-srv", "node-srv.team.internal", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cliLeaf, err := pkipkg.GenerateNodeCert(ca, "test-team", "node-cli", "node-cli.team.internal", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	srvCert, err := tls.X509KeyPair(srvLeaf.CertPEM, srvLeaf.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cliCert, err := tls.X509KeyPair(cliLeaf.CertPEM, cliLeaf.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM) {
		t.Fatal("ca pool append")
	}

	// Server side TLS — RequireAndVerifyClientCert mirrors the
	// production path on the unified listener (HA_PLAN3 §4.1).
	srvTLS := &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	cliTLS := &tls.Config{
		Certificates: []tls.Certificate{cliCert},
		RootCAs:      pool,
		ServerName:   "node-srv.team.internal",
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	stream, err := NewGRPCStreamLayer(GRPCStreamLayerConfig{
		AdvertiseAddr: listener.Addr().String(),
		Credentials:   credentials.NewTLS(cliTLS),
		AddrFor: func(_ hraft.ServerAddress) string {
			return listener.Addr().String()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	gsrv := grpc.NewServer(grpc.Creds(credentials.NewTLS(srvTLS)))
	internalspec.RegisterRaftTransportServiceServer(gsrv, stream)
	go func() { _ = gsrv.Serve(listener) }()
	t.Cleanup(gsrv.Stop)

	dialed, err := stream.Dial(hraft.ServerAddress(listener.Addr().String()), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialed.Close() })

	// Server-side accept; the gRPC stream handler pushes a streamConn
	// onto the layer's accept channel. Acquire it.
	type acceptResult struct {
		c   net.Conn
		err error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		c, e := stream.Accept()
		acceptCh <- acceptResult{c, e}
	}()

	if _, err := dialed.Write([]byte("hello-from-client")); err != nil {
		t.Fatalf("client Write: %v", err)
	}

	var server net.Conn
	select {
	case r := <-acceptCh:
		if r.err != nil {
			t.Fatalf("Accept: %v", r.err)
		}
		server = r.c
	case <-time.After(3 * time.Second):
		t.Fatal("Accept timed out")
	}
	t.Cleanup(func() { _ = server.Close() })

	buf := make([]byte, 64)
	n, err := server.Read(buf)
	if err != nil {
		t.Fatalf("server Read: %v", err)
	}
	if got := string(buf[:n]); got != "hello-from-client" {
		t.Errorf("server got %q, want %q", got, "hello-from-client")
	}

	if _, err := server.Write([]byte("ack-from-server")); err != nil {
		t.Fatalf("server Write: %v", err)
	}
	buf = make([]byte, 64)
	n, err = dialed.Read(buf)
	if err != nil {
		t.Fatalf("client Read: %v", err)
	}
	if got := string(buf[:n]); got != "ack-from-server" {
		t.Errorf("client got %q, want %q", got, "ack-from-server")
	}
}

func TestGRPCStreamLayer_DialFailsOnUnreachable(t *testing.T) {
	ca, _ := pkipkg.GenerateCA("test", time.Hour)
	leaf, _ := pkipkg.GenerateNodeCert(ca, "test", "n", "", time.Hour)
	cert, _ := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)
	stream, err := NewGRPCStreamLayer(GRPCStreamLayerConfig{
		AdvertiseAddr: "127.0.0.1:65501",
		Credentials: credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      pool,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	// 127.0.0.1:1 is "fail to connect" land — RST or refusal within tens of ms.
	if _, err := stream.Dial(hraft.ServerAddress("127.0.0.1:1"), 250*time.Millisecond); err == nil {
		t.Fatal("expected dial to fail against unreachable target, got nil")
	}
}
