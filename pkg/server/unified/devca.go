package unified

// Caddy-like local development CA ("tls internal"). A cached local root CA
// signs per-host leaf certificates on demand, so local/dev hosts get certs
// that all chain to ONE root the developer can trust — instead of a bare
// self-signed cert that no client trusts.
//
// The DevCA plugs into the boot path as the catch-all `GetCertificate` getter
// (the same slot the old self-signed cert used) ONLY when the operator opts in
// via hula_ssl.dev_ca.enabled. Per-host static / Cloudflare Origin CA certs
// registered via AddHostCertificate still win SNI before this fallback runs.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tlalocweb/hulation/log"
)

var devcaLog = log.GetTaggedLogger("devca", "Local development CA")

const (
	// devCARootValidity is how long the generated root CA is valid (10y).
	devCARootValidity = 10 * 365 * 24 * time.Hour
	// devCALeafValidity mirrors the ~825-day cap browsers enforce on leaf
	// certificates, so signed leaves behave like real-world server certs.
	devCALeafValidity = 825 * 24 * time.Hour
	// devCARootCertFile / devCARootKeyFile are the cached root PEM filenames.
	devCARootCertFile = "root.crt"
	devCARootKeyFile  = "root.key"
	// devCALocalCacheKey is the leaf-cache key used for handshakes with an
	// empty SNI (localhost / IP probes). Distinct from any real ServerName
	// because ServerName is never empty-string-cachable under a real host.
	devCALocalCacheKey = "\x00localhost"
)

// DevCA is an in-memory local development certificate authority. Its root
// cert+key are cached on disk so restarts reuse the SAME root (stable trust);
// signed leaves are cached in-memory per ServerName so repeated handshakes
// don't re-sign.
type DevCA struct {
	dir      string
	rootPath string
	keyPath  string

	rootCert *x509.Certificate
	rootKey  *ecdsa.PrivateKey
	rootPEM  []byte

	// signMu serialises leaf signing so two concurrent handshakes for the
	// same (uncached) ServerName sign at most one extra leaf.
	signMu sync.Mutex
	// leaves caches signed *tls.Certificate keyed by ServerName (or
	// devCALocalCacheKey for empty SNI).
	leaves sync.Map

	// signCount counts leaves actually signed — exposed to tests to prove
	// the in-memory cache avoids re-signing.
	signCount int64
}

// NewDevCA loads the cached root from dir if present, otherwise generates a new
// ECDSA P-256 root CA (10y, IsCA, KeyUsageCertSign) and persists cert+key PEM
// to dir (0600 key) so the next start reuses it.
func NewDevCA(dir string) (*DevCA, error) {
	if strings.TrimSpace(dir) == "" {
		dir = ".hula-devca"
	}
	ca := &DevCA{
		dir:      dir,
		rootPath: filepath.Join(dir, devCARootCertFile),
		keyPath:  filepath.Join(dir, devCARootKeyFile),
	}
	if err := ca.loadOrGenerateRoot(); err != nil {
		return nil, err
	}
	return ca, nil
}

// loadOrGenerateRoot reuses a cached root when both PEM files parse cleanly;
// otherwise it generates + persists a fresh root.
func (ca *DevCA) loadOrGenerateRoot() error {
	certPEM, cErr := os.ReadFile(ca.rootPath)
	keyPEM, kErr := os.ReadFile(ca.keyPath)
	if cErr == nil && kErr == nil {
		cert, key, err := parseDevCARoot(certPEM, keyPEM)
		if err == nil {
			ca.rootCert = cert
			ca.rootKey = key
			ca.rootPEM = certPEM
			devcaLog.Infof("loaded cached dev CA root from %s", ca.rootPath)
			return nil
		}
		devcaLog.Warnf("cached dev CA root at %s unusable (%v) — regenerating", ca.rootPath, err)
	}
	return ca.generateRoot()
}

// generateRoot creates a new root CA and writes cert+key PEM to disk.
func (ca *DevCA) generateRoot() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("devca: generate root key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return fmt.Errorf("devca: root serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Hula Dev CA",
			Organization: []string{"Hula Local Development CA"},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(devCARootValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("devca: create root cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("devca: parse root cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("devca: marshal root key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(ca.dir, 0o755); err != nil {
		return fmt.Errorf("devca: mkdir %q: %w", ca.dir, err)
	}
	if err := os.WriteFile(ca.rootPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("devca: write root cert %q: %w", ca.rootPath, err)
	}
	// 0600: the root's private key can mint trusted certs — keep it operator-only.
	if err := os.WriteFile(ca.keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("devca: write root key %q: %w", ca.keyPath, err)
	}
	ca.rootCert = cert
	ca.rootKey = key
	ca.rootPEM = certPEM
	devcaLog.Infof("generated new dev CA root at %s (valid %d years)", ca.rootPath, int(devCARootValidity.Hours()/24/365))
	return nil
}

// parseDevCARoot decodes a cached root cert+key PEM pair.
func parseDevCARoot(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cBlock, _ := pem.Decode(certPEM)
	if cBlock == nil || cBlock.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("root cert PEM missing CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(cBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse root cert: %w", err)
	}
	if !cert.IsCA {
		return nil, nil, fmt.Errorf("cached root cert is not a CA")
	}
	kBlock, _ := pem.Decode(keyPEM)
	if kBlock == nil {
		return nil, nil, fmt.Errorf("root key PEM undecodable")
	}
	key, err := x509.ParseECPrivateKey(kBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse root key: %w", err)
	}
	return cert, key, nil
}

// GetCertificate signs (or returns a cached) leaf for hello.ServerName. An
// empty ServerName yields a localhost/loopback leaf. The signature matches
// tls.Config.GetCertificate so it drops straight into the boot getCert slot.
func (ca *DevCA) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	serverName := ""
	if hello != nil {
		serverName = hello.ServerName
	}
	cacheKey := serverName
	if cacheKey == "" {
		cacheKey = devCALocalCacheKey
	}
	if v, ok := ca.leaves.Load(cacheKey); ok {
		return v.(*tls.Certificate), nil
	}

	ca.signMu.Lock()
	defer ca.signMu.Unlock()
	// Re-check under the lock: a concurrent caller may have signed while we
	// waited, so we don't sign the same leaf twice.
	if v, ok := ca.leaves.Load(cacheKey); ok {
		return v.(*tls.Certificate), nil
	}
	cert, err := ca.signLeaf(serverName)
	if err != nil {
		return nil, err
	}
	ca.leaves.Store(cacheKey, cert)
	return cert, nil
}

// signLeaf mints a fresh server-auth leaf for serverName, chained to the root
// (leaf DER + root DER in the chain).
func (ca *DevCA) signLeaf(serverName string) (*tls.Certificate, error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("devca: leaf key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, fmt.Errorf("devca: leaf serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: leafCommonName(serverName)},
		NotBefore:    now.Add(-1 * time.Hour),
		NotAfter:     now.Add(devCALeafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	applyLeafSANs(tmpl, serverName)

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.rootCert, &leafKey.PublicKey, ca.rootKey)
	if err != nil {
		return nil, fmt.Errorf("devca: sign leaf for %q: %w", serverName, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("devca: parse signed leaf: %w", err)
	}
	atomic.AddInt64(&ca.signCount, 1)
	return &tls.Certificate{
		Certificate: [][]byte{der, ca.rootCert.Raw},
		PrivateKey:  leafKey,
		Leaf:        leaf,
	}, nil
}

// applyLeafSANs populates DNS/IP SANs for the leaf. Empty ServerName (localhost
// / IP probes with no SNI) gets the loopback set; an IP literal ServerName goes
// in IPAddresses; anything else is a single DNS SAN.
func applyLeafSANs(tmpl *x509.Certificate, serverName string) {
	if serverName == "" {
		tmpl.DNSNames = []string{"localhost"}
		tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
		return
	}
	if ip := net.ParseIP(serverName); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
		return
	}
	tmpl.DNSNames = []string{serverName}
}

func leafCommonName(serverName string) string {
	if serverName == "" {
		return "localhost"
	}
	return serverName
}

// RootPEM returns the PEM-encoded root CA certificate, for emitting to the
// operator / installing into a trust store.
func (ca *DevCA) RootPEM() []byte { return ca.rootPEM }

// RootPath returns the on-disk path of the cached root CA certificate.
func (ca *DevCA) RootPath() string { return ca.rootPath }

// signCountLoad returns the number of leaves signed so far (test helper).
func (ca *DevCA) signCountLoad() int64 { return atomic.LoadInt64(&ca.signCount) }

// TrustInstructions returns copy-pasteable commands to trust the root manually
// on the current platform. Logged at boot so operators can remove browser
// warnings without the invasive auto-install.
func (ca *DevCA) TrustInstructions() string {
	switch runtime.GOOS {
	case "linux":
		return fmt.Sprintf("sudo cp %s /usr/local/share/ca-certificates/hula-devca.crt && sudo update-ca-certificates", ca.rootPath)
	case "darwin":
		return fmt.Sprintf("sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s", ca.rootPath)
	default:
		return fmt.Sprintf("import %s into your OS/browser trust store as a trusted root CA", ca.rootPath)
	}
}

// InstallTrust installs the root into the OS trust store, mkcert-style. Only
// ever called when hula_ssl.dev_ca.install_trust is true. On unsupported
// platforms or on error it logs a clear manual-install instruction and returns
// the error (it never panics).
func (ca *DevCA) InstallTrust() error {
	switch runtime.GOOS {
	case "linux":
		return ca.installTrustLinux()
	case "darwin":
		return ca.installTrustDarwin()
	default:
		err := fmt.Errorf("devca: automatic trust install unsupported on %s", runtime.GOOS)
		devcaLog.Warnf("%v — trust the root manually: %s", err, ca.TrustInstructions())
		return err
	}
}

func (ca *DevCA) installTrustLinux() error {
	const dest = "/usr/local/share/ca-certificates/hula-devca.crt"
	if err := os.WriteFile(dest, ca.rootPEM, 0o644); err != nil {
		werr := fmt.Errorf("devca: write %s: %w", dest, err)
		devcaLog.Warnf("%v — trust the root manually: %s", werr, ca.TrustInstructions())
		return werr
	}
	out, err := exec.Command("update-ca-certificates").CombinedOutput()
	if err != nil {
		werr := fmt.Errorf("devca: update-ca-certificates: %w (%s)", err, strings.TrimSpace(string(out)))
		devcaLog.Warnf("%v — trust the root manually: %s", werr, ca.TrustInstructions())
		return werr
	}
	devcaLog.Infof("installed dev CA root into system trust store (%s)", dest)
	return nil
}

func (ca *DevCA) installTrustDarwin() error {
	out, err := exec.Command(
		"security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain", ca.rootPath,
	).CombinedOutput()
	if err != nil {
		werr := fmt.Errorf("devca: security add-trusted-cert: %w (%s)", err, strings.TrimSpace(string(out)))
		devcaLog.Warnf("%v — trust the root manually: %s", werr, ca.TrustInstructions())
		return werr
	}
	devcaLog.Infof("installed dev CA root into System keychain (%s)", ca.rootPath)
	return nil
}

// randSerial returns a random 128-bit certificate serial number.
func randSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
