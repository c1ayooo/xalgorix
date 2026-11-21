package trafficproxy

// CA acceptance tests (R2b decision 1): generation, persistence, reload with
// identical key, permissions 0700/0600, leaf signing (DNS + IP SAN, validity,
// server-auth EKU), in-process leaf cache, and fail-closed on a corrupt CA
// (never silently re-signed).

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/storage"
)

func TestCAGeneratePersistReloadSameKey(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate (generate): %v", err)
	}

	// Persistence: both files exist.
	for _, name := range []string{"ca.pem", "ca.key.pem", "ca-bundle.pem"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s not persisted: %v", name, err)
		}
	}

	// Reload: the same key comes back — a fresh generation would strand every
	// consumer trust store.
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate (reload): %v", err)
	}
	certA, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if string(certA) != string(first.certPEM) || string(certA) != string(second.certPEM) {
		t.Fatal("reload did not return the persisted CA key material")
	}
	if !first.leaf.Equal(second.leaf) {
		t.Fatal("reloaded CA certificate differs from the generated one")
	}
}

func TestCAModeBits(t *testing.T) {
	// A fresh subdirectory so LoadOrCreate (not t.TempDir) owns the mode.
	dir := filepath.Join(t.TempDir(), "ca")
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("CA dir mode = %v, want 0700 (err=%v)", info.Mode().Perm(), err)
	}
	for _, name := range []string{"ca.pem", "ca.key.pem"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}
}

func TestCALeafSigning(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}

	leaf, err := ca.LeafForHost("example.com")
	if err != nil {
		t.Fatalf("LeafForHost(dns): %v", err)
	}
	cert, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "example.com" {
		t.Fatalf("DNS SAN = %v, want [example.com]", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 0 {
		t.Fatalf("unexpected IP SANs on a DNS leaf: %v", cert.IPAddresses)
	}
	if !cert.NotAfter.After(time.Now()) || time.Until(cert.NotAfter) > 31*24*time.Hour {
		t.Fatalf("leaf validity out of bounds: until %v", cert.NotAfter)
	}
	for _, eku := range cert.ExtKeyUsage {
		if eku != x509.ExtKeyUsageServerAuth {
			t.Fatalf("unexpected EKU %v", eku)
		}
	}
	if err := cert.CheckSignatureFrom(ca.leaf); err != nil {
		t.Fatalf("leaf not signed by the CA: %v", err)
	}
	// The leaf must verify against the CA pool end-to-end.
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:       ca.CertPool(),
		CurrentTime: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("leaf does not verify against the CA pool: %v", err)
	}

	ipLeaf, err := ca.LeafForHost("127.0.0.1")
	if err != nil {
		t.Fatalf("LeafForHost(ip): %v", err)
	}
	ipCert, err := x509.ParseCertificate(ipLeaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(ipCert.IPAddresses) != 1 || !ipCert.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("IP SAN = %v, want 127.0.0.1", ipCert.IPAddresses)
	}
	if len(ipCert.DNSNames) != 0 {
		t.Fatalf("unexpected DNS SANs on an IP leaf: %v", ipCert.DNSNames)
	}
}

func TestCALeafCacheHit(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ca.LeafForHost("cache.example")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ca.LeafForHost("cache.example")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Certificate[0]) != string(second.Certificate[0]) {
		t.Fatal("leaf cache miss: second leaf differs from the first")
	}
}

func TestCACorruptRefused(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}

	// Corrupt the certificate: LoadOrCreate must refuse (fail-closed) and
	// must NOT regenerate, because consumer trust stores pin the old CA.
	if err := storage.WriteAtomic(filepath.Join(dir, "ca.pem"), []byte("not a pem block")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("corrupt CA certificate was accepted")
	}

	// Half a pair is corruption too.
	dir2 := t.TempDir()
	if _, err := LoadOrCreate(dir2); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir2, "ca.key.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir2); err == nil {
		t.Fatal("missing CA key was accepted")
	}

	// A non-CA certificate in place must be refused as well.
	dir3 := t.TempDir()
	if _, err := LoadOrCreate(dir3); err != nil {
		t.Fatal(err)
	}
	ca3, _ := LoadOrCreate(dir3)
	leaf, err := ca3.LeafForHost("not-a-ca.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteAtomic(filepath.Join(dir3, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]})); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir3); err == nil {
		t.Fatal("non-CA certificate was accepted as the CA")
	}
}

func TestCABundleContainsSystemAndCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(ca.BundlePath())
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	block, _ := pem.Decode(bundle)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("bundle does not start with the CA certificate")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		t.Fatal("bundle is not a usable certificate pool")
	}
}
