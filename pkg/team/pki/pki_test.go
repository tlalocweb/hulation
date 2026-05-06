package pki

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateCA_RoundTrips(t *testing.T) {
	ca, err := GenerateCA("team-abc", time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if ca.Cert == nil || len(ca.CertPEM) == 0 || len(ca.KeyPEM) == 0 {
		t.Fatalf("ca returned with empty fields: %+v", ca)
	}
	if !ca.Cert.IsCA {
		t.Fatalf("ca cert not flagged IsCA")
	}
	if got, want := ca.Cert.Subject.CommonName, "hula-team-ca/team-abc"; got != want {
		t.Errorf("CN got=%q want=%q", got, want)
	}

	block, _ := pem.Decode(ca.CertPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("ca CertPEM not a CERTIFICATE block")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		t.Errorf("ca cert reparse: %v", err)
	}
}

func TestGenerateCA_RejectsEmptyTeamID(t *testing.T) {
	if _, err := GenerateCA("", time.Hour); err == nil {
		t.Fatal("expected error on empty teamID")
	}
}

func TestGenerateNodeCert_VerifiesAgainstCA(t *testing.T) {
	ca, err := GenerateCA("team-xyz", time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	node, err := GenerateNodeCert(ca, "team-xyz", "node-east", "node-east.example.com", time.Hour)
	if err != nil {
		t.Fatalf("GenerateNodeCert: %v", err)
	}

	// Verify via the bundle path so we exercise the same code that
	// boot will run.
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(caPath, ca.CertPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, node.CertPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, node.KeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := LoadBundle(caPath, certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	if b.CAPool == nil || len(b.Cert) == 0 || len(b.Key) == 0 {
		t.Fatalf("bundle missing fields")
	}
}

func TestLoadBundle_RejectsWrongCA(t *testing.T) {
	caA, _ := GenerateCA("team-A", time.Hour)
	caB, _ := GenerateCA("team-B", time.Hour)
	node, err := GenerateNodeCert(caA, "team-A", "node-1", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// CA file is from team-B, but cert is signed by team-A — must reject.
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), caB.CertPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), node.CertPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), node.KeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(
		filepath.Join(dir, "ca.pem"),
		filepath.Join(dir, "cert.pem"),
		filepath.Join(dir, "key.pem"),
	); err == nil {
		t.Fatal("expected verify failure for wrong-CA bundle, got nil")
	}
}

func TestGenerateBootstrapToken_Length(t *testing.T) {
	tok, err := GenerateBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != BootstrapTokenBytes {
		t.Errorf("token length got=%d want=%d", len(tok), BootstrapTokenBytes)
	}
	tok2, _ := GenerateBootstrapToken()
	if string(tok) == string(tok2) {
		t.Errorf("two bootstrap tokens identical — randomness broken")
	}
}

func TestWriteBundle_LayoutAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	ca, _ := GenerateCA("team-1", time.Hour)
	n1, _ := GenerateNodeCert(ca, "team-1", "node-east", "", time.Hour)
	n2, _ := GenerateNodeCert(ca, "team-1", "node-west", "", time.Hour)
	tok, _ := GenerateBootstrapToken()

	if err := WriteBundle(dir, "team-1", ca, []*NodeCert{n1, n2}, tok); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	for _, want := range []string{
		"ca.pem", "ca.key", "bootstrap-token", "team-id",
		"node-east/cert.pem", "node-east/key.pem", "node-east/ca.pem",
		"node-west/cert.pem", "node-west/key.pem", "node-west/ca.pem",
	} {
		p := filepath.Join(dir, want)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}

	// ca.key + per-node key.pem must be 0o600 — the bundle is sensitive material.
	for _, p := range []string{"ca.key", "node-east/key.pem", "node-west/key.pem", "bootstrap-token"} {
		info, err := os.Stat(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s perm got=%o want=0600", p, mode)
		}
	}
}

func TestWriteBundle_RefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir() // already exists, but empty — that's allowed
	if err := os.WriteFile(filepath.Join(dir, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ca, _ := GenerateCA("t", time.Hour)
	tok, _ := GenerateBootstrapToken()
	if err := WriteBundle(dir, "t", ca, nil, tok); err == nil {
		t.Fatal("expected refusal when destination is non-empty")
	}
}

func TestNodeCert_SANIncludesTeamAndNode(t *testing.T) {
	ca, _ := GenerateCA("t1", time.Hour)
	node, err := GenerateNodeCert(ca, "t1", "n1", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(node.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"n1" + SANInternalSuffix, "t1/n1" + SANInternalSuffix}
	for _, w := range want {
		found := false
		for _, dns := range cert.DNSNames {
			if dns == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("SAN missing %q (got %v)", w, cert.DNSNames)
		}
	}
}
