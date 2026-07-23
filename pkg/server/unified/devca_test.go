package unified

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// verifyLeafAgainstRoot asserts the leaf in cert chains to root and (when
// dnsName != "") is valid for that name under ExtKeyUsageServerAuth.
func verifyLeafAgainstRoot(t *testing.T, cert *tls.Certificate, root *x509.Certificate, dnsName string) *x509.Certificate {
	t.Helper()
	if len(cert.Certificate) != 2 {
		t.Fatalf("leaf chain len = %d, want 2 (leaf + root)", len(cert.Certificate))
	}
	if !bytes.Equal(cert.Certificate[1], root.Raw) {
		t.Fatal("second chain entry is not the root cert DER")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   dnsName,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("leaf verify against root (dnsName=%q): %v", dnsName, err)
	}
	return leaf
}

// TestDevCA_SignsAndVerifiesLeaf: a fresh dev CA signs a leaf for a host and
// the leaf verifies against the root, with the host in its SANs.
func TestDevCA_SignsAndVerifiesLeaf(t *testing.T) {
	ca, err := NewDevCA(t.TempDir())
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	cert, err := ca.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.test"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	leaf := verifyLeafAgainstRoot(t, cert, ca.rootCert, "example.test")

	found := false
	for _, n := range leaf.DNSNames {
		if n == "example.test" {
			found = true
		}
	}
	if !found {
		t.Errorf("leaf DNSNames %v missing ServerName example.test", leaf.DNSNames)
	}
}

// TestDevCA_RootPersistedAndReused: a second DevCA built from the same dir
// reuses the SAME root (identical DER), and leaves from either verify against
// that shared root.
func TestDevCA_RootPersistedAndReused(t *testing.T) {
	dir := t.TempDir()
	ca1, err := NewDevCA(dir)
	if err != nil {
		t.Fatalf("NewDevCA #1: %v", err)
	}
	ca2, err := NewDevCA(dir)
	if err != nil {
		t.Fatalf("NewDevCA #2: %v", err)
	}
	if !bytes.Equal(ca1.rootCert.Raw, ca2.rootCert.Raw) {
		t.Fatal("second DevCA generated a different root — root was not reused from disk")
	}
	if !bytes.Equal(ca1.RootPEM(), ca2.RootPEM()) {
		t.Fatal("RootPEM differs between reloads")
	}

	// A leaf signed by ca2 must verify against ca1's root (they are the same).
	cert, err := ca2.GetCertificate(&tls.ClientHelloInfo{ServerName: "reuse.test"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	verifyLeafAgainstRoot(t, cert, ca1.rootCert, "reuse.test")

	// The cached root cert+key files should live under the requested dir.
	if got := ca1.RootPath(); filepath.Dir(got) != dir {
		t.Errorf("RootPath %q not under dir %q", got, dir)
	}
}

// TestDevCA_LeafCaching: repeated handshakes for the same SNI return the SAME
// cached leaf and do not re-sign.
func TestDevCA_LeafCaching(t *testing.T) {
	ca, err := NewDevCA(t.TempDir())
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	hello := &tls.ClientHelloInfo{ServerName: "cache.test"}
	c1, err := ca.GetCertificate(hello)
	if err != nil {
		t.Fatalf("GetCertificate #1: %v", err)
	}
	c2, err := ca.GetCertificate(hello)
	if err != nil {
		t.Fatalf("GetCertificate #2: %v", err)
	}
	if c1 != c2 {
		t.Error("second GetCertificate returned a different *tls.Certificate; leaf was not cached")
	}
	if n := ca.signCountLoad(); n != 1 {
		t.Errorf("signCount = %d, want 1 (cache should avoid the second sign)", n)
	}

	// A different SNI signs a distinct leaf.
	if _, err := ca.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.test"}); err != nil {
		t.Fatalf("GetCertificate other: %v", err)
	}
	if n := ca.signCountLoad(); n != 2 {
		t.Errorf("signCount = %d, want 2 after a distinct SNI", n)
	}
}

// TestDevCA_EmptyServerName_LocalhostLeaf: an empty SNI yields a localhost /
// loopback leaf that verifies.
func TestDevCA_EmptyServerName_LocalhostLeaf(t *testing.T) {
	ca, err := NewDevCA(t.TempDir())
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	cert, err := ca.GetCertificate(&tls.ClientHelloInfo{ServerName: ""})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	leaf := verifyLeafAgainstRoot(t, cert, ca.rootCert, "localhost")

	hasLocalhost := false
	for _, n := range leaf.DNSNames {
		if n == "localhost" {
			hasLocalhost = true
		}
	}
	if !hasLocalhost {
		t.Errorf("empty-SNI leaf DNSNames %v missing localhost", leaf.DNSNames)
	}
	hasLoopbackIP := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.IPv4(127, 0, 0, 1)) {
			hasLoopbackIP = true
		}
	}
	if !hasLoopbackIP {
		t.Errorf("empty-SNI leaf IPAddresses %v missing 127.0.0.1", leaf.IPAddresses)
	}

	// A nil ClientHelloInfo must not panic and behaves like empty SNI.
	if _, err := ca.GetCertificate(nil); err != nil {
		t.Fatalf("GetCertificate(nil): %v", err)
	}
}

// TestDevCA_TrustInstructions_NonDestructive: TrustInstructions returns a
// clear, platform-appropriate hint referencing the root path. We deliberately
// do NOT call InstallTrust (it mutates the OS trust store).
func TestDevCA_TrustInstructions_NonDestructive(t *testing.T) {
	ca, err := NewDevCA(t.TempDir())
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	hint := ca.TrustInstructions()
	if hint == "" {
		t.Fatal("TrustInstructions returned empty string")
	}
	if !strings.Contains(hint, ca.RootPath()) {
		t.Errorf("TrustInstructions %q does not reference root path %q", hint, ca.RootPath())
	}
	// On the platforms we automate, the hint names the OS trust tooling.
	switch runtime.GOOS {
	case "linux":
		if !strings.Contains(hint, "update-ca-certificates") {
			t.Errorf("linux hint missing update-ca-certificates: %q", hint)
		}
	case "darwin":
		if !strings.Contains(hint, "add-trusted-cert") {
			t.Errorf("darwin hint missing add-trusted-cert: %q", hint)
		}
	}
}

// TestDevCA_MismatchedCachedPairRegenerates: a cached root.crt/root.key that
// don't match (e.g. cert from one run, key from another) is treated as cache
// corruption — the dev CA regenerates a consistent root instead of signing
// leaves whose chain would be unverifiable.
func TestDevCA_MismatchedCachedPairRegenerates(t *testing.T) {
	dirA := t.TempDir()
	caA, err := NewDevCA(dirA)
	if err != nil {
		t.Fatalf("NewDevCA A: %v", err)
	}
	dirB := t.TempDir()
	caB, err := NewDevCA(dirB)
	if err != nil {
		t.Fatalf("NewDevCA B: %v", err)
	}

	// Corrupt dirA: keep A's cert but overwrite A's key with B's (mismatch).
	keyB, err := os.ReadFile(caB.keyPath)
	if err != nil {
		t.Fatalf("read B key: %v", err)
	}
	if err := os.WriteFile(caA.keyPath, keyB, 0o600); err != nil {
		t.Fatalf("corrupt A key: %v", err)
	}

	// Reload dirA: the mismatch must be detected and a fresh, consistent root
	// generated, so a leaf verifies against the (regenerated) root.
	caA2, err := NewDevCA(dirA)
	if err != nil {
		t.Fatalf("NewDevCA A reload: %v", err)
	}
	cert, err := caA2.GetCertificate(&tls.ClientHelloInfo{ServerName: "x.test"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	verifyLeafAgainstRoot(t, cert, caA2.rootCert, "x.test")
	if bytes.Equal(caA2.rootCert.Raw, caA.rootCert.Raw) {
		t.Fatal("expected a regenerated root after cert/key mismatch, got the original")
	}
}
