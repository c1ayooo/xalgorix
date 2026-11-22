package trafficproxy

// Traffic-proxy CA management (REDESIGN §4, R2b decision 1): a pure
// stdlib CA — crypto/x509 self-signed ECDSA P-256 root plus per-host leaf
// certificates — persisted under ~/.xalgorix/ca (override XALGORIX_CA_DIR).
//
// Durability and permission contract (AGENTS.md invariant 3):
//   - directory 0700 via storage.EnsureSecureDir, files 0600 via
//     storage.WriteAtomic (O_EXCL + fsync + rename + dir fsync);
//   - a CORRUPT CA is fail-closed for interception: LoadOrCreate refuses to
//     start the MITM and never silently re-signs, because every consumer
//     trust store (browser profile, curl --cacert, python bundles) already
//     pins the old CA and a re-signed one would strand them.
//
// A combined trust bundle (traffic-proxy CA + system bundle) is written
// alongside the CA so python's SSL_CERT_FILE can point at a file that still
// trusts the rest of the internet.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/storage"
)

const (
	caCertFile   = "ca.pem"
	caKeyFile    = "ca.key.pem"
	caBundleFile = "ca-bundle.pem"

	caCommonName    = "Xalgorix Traffic Proxy CA"
	caValidity      = 10 * 365 * 24 * time.Hour
	leafValidity    = 30 * 24 * time.Hour
	leafCacheMaxLen = 4096

	// systemBundleCandidates are probed in order for the OS trust store used
	// to build the combined bundle (Debian/Ubuntu, RHEL, Alpine).
	systemBundleCandidates = "/etc/ssl/certs/ca-certificates.crt"
)

// CA is a loaded traffic-proxy certificate authority. It is safe for
// concurrent use.
type CA struct {
	certPEM  []byte
	keyPEM   []byte
	cert     tls.Certificate
	leaf     *x509.Certificate
	key      *ecdsa.PrivateKey
	pool     *x509.CertPool
	dir      string
	bundleOK bool

	mu     sync.Mutex
	leaves map[string]tls.Certificate
}

// LoadOrCreate loads the CA from dir, or creates it on first use. A missing
// pair is generated; a present-but-unusable pair is a hard error (fail-closed,
// never re-signed). Only one of the pair present is also treated as corrupt.
func LoadOrCreate(dir string) (*CA, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("trafficproxy: CA directory is empty")
	}
	if err := storage.EnsureSecureDir(dir); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, caCertFile)
	keyPath := filepath.Join(dir, caKeyFile)

	certData, certErr := os.ReadFile(certPath)
	keyData, keyErr := os.ReadFile(keyPath)
	switch {
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		ca, err := generateCA(dir)
		if err != nil {
			return nil, err
		}
		if err := ca.writeBundle(); err != nil {
			return nil, err
		}
		return ca, nil
	case certErr != nil:
		return nil, fmt.Errorf("trafficproxy: read CA certificate: %w", certErr)
	case keyErr != nil:
		return nil, fmt.Errorf("trafficproxy: read CA key: %w", keyErr)
	}

	ca, err := loadCA(dir, certData, keyData)
	if err != nil {
		return nil, err // corrupt CA: refuse, do not re-sign
	}
	if err := ca.writeBundle(); err != nil {
		return nil, err
	}
	return ca, nil
}

// generateCA creates a fresh self-signed ECDSA P-256 CA and persists it
// atomically before it is ever used to sign a leaf.
func generateCA(dir string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("trafficproxy: generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: caCommonName, Organization: []string{"Xalgorix"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("trafficproxy: create CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("trafficproxy: marshal CA key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	ca, err := loadCA(dir, certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	// Persist BEFORE returning: a CA that was used to sign leaves but failed
	// to reach disk would be re-generated on the next start and strand the
	// trust stores that pinned the first one.
	if err := storage.WriteAtomic(filepath.Join(dir, caCertFile), certPEM); err != nil {
		return nil, fmt.Errorf("trafficproxy: persist CA certificate: %w", err)
	}
	if err := storage.WriteAtomic(filepath.Join(dir, caKeyFile), keyPEM); err != nil {
		return nil, fmt.Errorf("trafficproxy: persist CA key: %w", err)
	}
	return ca, nil
}

// loadCA parses PEM material into a usable CA. Any parse or pairing failure
// is surfaced verbatim so the caller can refuse MITM (fail-closed).
func loadCA(dir string, certPEM, keyPEM []byte) (*CA, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("trafficproxy: CA keypair unusable: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("trafficproxy: CA certificate unparsable: %w", err)
	}
	if !leaf.IsCA {
		return nil, errors.New("trafficproxy: stored certificate is not a CA — refusing MITM")
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("trafficproxy: CA key is not ECDSA — refusing MITM")
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &CA{
		certPEM:  certPEM,
		keyPEM:   keyPEM,
		cert:     pair,
		leaf:     leaf,
		key:      key,
		pool:     pool,
		dir:      dir,
		leaves:   make(map[string]tls.Certificate),
		bundleOK: false,
	}, nil
}

// writeBundle refreshes the combined trust bundle (traffic-proxy CA first,
// then the OS bundle when readable). The bundle is optional convenience for
// python's SSL_CERT_FILE; failure here does not disable the CA itself.
func (c *CA) writeBundle() error {
	var b []byte
	b = append(b, c.certPEM...)
	if sys, err := os.ReadFile(systemBundleCandidates); err == nil && len(sys) > 0 {
		b = append(b, sys...)
	}
	if err := storage.WriteAtomic(filepath.Join(c.dir, caBundleFile), b); err != nil {
		return fmt.Errorf("trafficproxy: write CA bundle: %w", err)
	}
	c.bundleOK = true
	return nil
}

// LeafForHost returns a leaf certificate for host (a bare hostname or IP),
// signed by the traffic-proxy CA. Leaves carry a DNS SAN for names and an IP
// SAN for addresses, are valid for leafValidity, and are cached per host in
// process memory.
func (c *CA) LeafForHost(host string) (tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return tls.Certificate{}, errors.New("trafficproxy: empty leaf host")
	}
	c.mu.Lock()
	cached, ok := c.leaves[host]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("trafficproxy: generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.leaf, &leafKey.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("trafficproxy: sign leaf for %q: %w", host, err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("trafficproxy: marshal leaf key: %w", err)
	}
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("trafficproxy: leaf keypair for %q: %w", host, err)
	}

	c.mu.Lock()
	// Bounded cache: evict everything when it grows past the cap. Leaves are
	// cheap to re-sign and the cap only guards against pathological churn.
	if len(c.leaves) >= leafCacheMaxLen {
		c.leaves = make(map[string]tls.Certificate)
	}
	c.leaves[host] = pair
	c.mu.Unlock()
	return pair, nil
}

// CertPool returns the trust pool containing the traffic-proxy CA.
func (c *CA) CertPool() *x509.CertPool {
	if c == nil {
		return nil
	}
	return c.pool
}

// CertPath is the CA certificate path consumers can pass to curl --cacert.
func (c *CA) CertPath() string {
	if c == nil {
		return ""
	}
	return filepath.Join(c.dir, caCertFile)
}

// BundlePath is the combined trust bundle path for python SSL_CERT_FILE.
func (c *CA) BundlePath() string {
	if c == nil || !c.bundleOK {
		return ""
	}
	return filepath.Join(c.dir, caBundleFile)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("trafficproxy: random serial: %w", err)
	}
	return serial, nil
}
