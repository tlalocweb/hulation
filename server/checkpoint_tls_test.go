package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/config"
)

func checkpointTestIdentity(t *testing.T) (*config.ProxyUpstreamTLS, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "checkpoint test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "checkpoint proxy"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	cert, err := x509.CreateCertificate(rand.Reader, leaf, ca, leafKey.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certPath, keyPath := filepath.Join(directory, "proxy.crt"), filepath.Join(directory, "proxy.key")
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	return &config.ProxyUpstreamTLS{ClientCert: certPath, ClientKey: keyPath, CheckpointBinding: true}, roots
}

func TestCheckpointTLSBindingSurvivesTwoVerifiedTLSHops(t *testing.T) {
	identity, clientRoots := checkpointTestIdentity(t)
	var seen sync.Map
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.VerifiedChains) == 0 {
			t.Error("upstream client identity was not verified")
			return
		}
		encoded := r.Header.Get(checkpointBindingHeader)
		if len(encoded) != 67 || encoded[:3] != "v1." {
			t.Error("missing frontend binding")
			return
		}
		frontend, err := hex.DecodeString(encoded[3:])
		if err != nil {
			t.Error(err)
			return
		}
		own, err := r.TLS.ExportKeyingMaterial("EXPORTER-Mitosu-Checkpoint-v2", nil, 32)
		if err != nil {
			t.Error(err)
			return
		}
		if hmac.Equal(own, frontend) {
			t.Error("proxy reused the backend TLS binding")
			return
		}
		if _, duplicate := seen.LoadOrStore(encoded, true); duplicate {
			t.Error("different clients shared a binding")
		}
		mac := hmac.New(sha256.New, frontend)
		_, _ = mac.Write([]byte("checkpoint test receipt"))
		_, _ = io.WriteString(w, hex.EncodeToString(mac.Sum(nil)))
	}))
	backend.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots}
	backend.StartTLS()
	defer backend.Close()
	backendRoots := x509.NewCertPool()
	backendRoots.AddCert(backend.Certificate())
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: backendRoots, MinVersion: tls.VersionTLS12}
	target, _ := url.Parse(backend.URL)
	handler, err := checkpointProxyWithTransport(target, identity, base)
	if err != nil {
		t.Fatal(err)
	}
	frontend := httptest.NewTLSServer(handler)
	defer frontend.Close()
	frontendRoots := x509.NewCertPool()
	frontendRoots.AddCert(frontend.Certificate())
	for range 2 {
		var clientBinding []byte
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: frontendRoots}, DisableKeepAlives: true}}
		request, _ := http.NewRequest(http.MethodGet, frontend.URL+"/checkpoints", nil)
		request.Header.Set("Connection", "Upgrade, "+checkpointBindingHeader)
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set(checkpointBindingHeader, "attacker supplied value")
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
			TLSHandshakeDone: func(state tls.ConnectionState, err error) {
				if err != nil {
					t.Error(err)
					return
				}
				clientBinding, err = state.ExportKeyingMaterial("EXPORTER-Mitosu-Checkpoint-v2", nil, 32)
				if err != nil {
					t.Error(err)
				}
			},
		}))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, clientBinding)
		_, _ = mac.Write([]byte("checkpoint test receipt"))
		if response.StatusCode != 200 || string(bytes) != hex.EncodeToString(mac.Sum(nil)) {
			t.Fatal("receipt did not match the frontend client's binding")
		}
	}
}

func TestCheckpointTLSBindingIsStrippedUnlessEnabled(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(checkpointBindingHeader) != "" {
			t.Error("untrusted binding reached upstream")
		}
		w.WriteHeader(204)
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	request := httptest.NewRequest(http.MethodGet, "http://relay.test/checkpoints", nil)
	request.Header.Set(checkpointBindingHeader, "untrusted")
	response := httptest.NewRecorder()
	newPlainProxy(target).ServeHTTP(response, request)
	if response.Code != 204 {
		t.Fatalf("status %d", response.Code)
	}
}

func TestCheckpointTLSBindingRejectsCleartextAndDisabledVerification(t *testing.T) {
	identity, _ := checkpointTestIdentity(t)
	target, _ := url.Parse("http://relay.test")
	if _, err := newCheckpointProxy(target, identity); err == nil {
		t.Fatal("cleartext upstream accepted")
	}
	target, _ = url.Parse("https://relay.test")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	if _, err := checkpointProxyWithTransport(target, identity, transport); err == nil {
		t.Fatal("unverified upstream accepted")
	}
	transport.TLSClientConfig = &tls.Config{}
	handler, err := checkpointProxyWithTransport(target, identity, transport)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://frontend.test/checkpoints", nil)
	request.Header.Set("Upgrade", "websocket")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 502 {
		t.Fatalf("cleartext frontend status %d", response.Code)
	}
}
