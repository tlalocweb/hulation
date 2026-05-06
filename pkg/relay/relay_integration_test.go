package relay_test

// End-to-end integration test for HA Stage 3.6: outbox → drainer →
// gRPC RelayService.RecordEventBatch → fakeWriter. Runs entirely
// in-process so no Docker / ClickHouse is needed.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/tlalocweb/hulation/model"
	relayimpl "github.com/tlalocweb/hulation/pkg/api/v1/relay"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/pkg/relay"
	pkipkg "github.com/tlalocweb/hulation/pkg/team/pki"
)

type recordingWriter struct {
	mu  sync.Mutex
	got []string
}

func (w *recordingWriter) Insert(_ context.Context, encoded [][]byte) (uint32, uint32, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range encoded {
		ev, err := relay.DecodeEvent(b)
		if err != nil {
			continue
		}
		w.got = append(w.got, ev.ID)
	}
	return uint32(len(encoded)), 0, nil
}

func (w *recordingWriter) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := append([]string(nil), w.got...)
	return out
}

func TestRelay_EndToEnd_OutboxToCH(t *testing.T) {
	// Stand up a CA + leaf cert; use them on both server and
	// client sides (one cert is fine — the chain validates).
	ca, err := pkipkg.GenerateCA("relay-test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := pkipkg.GenerateNodeCert(ca, "relay-test", "ch-node", "ch.team.internal", time.Hour)
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
	dialTLS, err := pkipkg.PeerDialTLSConfig(&pkipkg.Bundle{
		CACertPEM: ca.CertPEM,
		CAPool:    pool,
		Cert:      leaf.CertPEM,
		Key:       leaf.KeyPEM,
	})
	if err != nil {
		t.Fatal(err)
	}

	// CH-side server hosting RelayService backed by recordingWriter.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	w := &recordingWriter{}
	gsrv := grpc.NewServer(grpc.Creds(credentials.NewTLS(srvTLS)))
	internalspec.RegisterRelayServiceServer(gsrv, relayimpl.New(w))
	go func() { _ = gsrv.Serve(lis) }()
	t.Cleanup(gsrv.Stop)

	// Sender + drainer + outbox on the non-CH side.
	o, err := relay.New(relay.Config{RingSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })

	creds := credentials.NewTLS(dialTLS)
	addr := lis.Addr().String()
	var dials atomic.Uint64
	sender := func(ctx context.Context, batch []*model.Event) error {
		dials.Add(1)
		conn, err := grpc.DialContext(ctx, addr,
			grpc.WithTransportCredentials(creds),
			grpc.WithBlock(),
		)
		if err != nil {
			return err
		}
		defer conn.Close()
		encoded := make([][]byte, 0, len(batch))
		for _, ev := range batch {
			b, err := relay.EncodeEvent(ev)
			if err != nil {
				return err
			}
			encoded = append(encoded, b)
		}
		_, err = internalspec.NewRelayServiceClient(conn).RecordEventBatch(ctx,
			&internalspec.RecordEventBatchRequest{
				Events:       encoded,
				SourceNodeId: "test-source",
			})
		return err
	}

	d := relay.Start(context.Background(), o, sender, relay.DrainerConfig{
		BatchSize:   8,
		IdlePoll:    10 * time.Millisecond,
		BackoffMin:  10 * time.Millisecond,
		BackoffMax:  100 * time.Millisecond,
		BackoffMult: 2,
	})
	t.Cleanup(d.Stop)

	// Pump 10 events; expect all 10 to land at the writer.
	for i := 0; i < 10; i++ {
		_ = o.Enqueue(&model.Event{HModel: model.HModel{ID: tag(i)}})
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(w.snapshot()) >= 10 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := w.snapshot()
	if len(got) != 10 {
		t.Fatalf("writer got %d events, want 10 (snapshot=%v dials=%d)", len(got), got, dials.Load())
	}
	for i, id := range got {
		if id != tag(i) {
			t.Errorf("order broken at idx %d: got=%q want=%q", i, id, tag(i))
			break
		}
	}
	if d.Successes() == 0 {
		t.Error("Successes counter never incremented")
	}
}

func tag(i int) string {
	const hex = "0123456789abcdef"
	return string([]byte{'e', hex[i%16]})
}
