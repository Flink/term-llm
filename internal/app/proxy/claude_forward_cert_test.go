package proxy

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestClaudeForwardCertificateRenewsLeafWithoutChangingTrust(t *testing.T) {
	dir := claudeForwardPrivateTempDir(t)
	ca, err := openClaudeForwardCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &claudeForwarder{ca: ca}
	original, err := f.certificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Advance only the cache's expiry metadata, not wall time or certificate DER.
	expiring := *original.Leaf
	expiring.NotAfter = time.Now().Add(time.Hour)
	f.certificates.leaf.Leaf = &expiring
	const workers = 8
	results := make(chan []byte, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leaf, err := f.certificate(dir)
			if err != nil {
				t.Error(err)
				return
			}
			results <- leaf.Certificate[0]
		}()
	}
	wg.Wait()
	close(results)
	var renewed []byte
	for der := range results {
		if bytes.Equal(der, original.Certificate[0]) {
			t.Error("expiring leaf was reused")
		}
		if renewed != nil && !bytes.Equal(renewed, der) {
			t.Error("concurrent handshakes rotated more than once")
		}
		renewed = der
	}
	cert, err := x509.ParseCertificate(renewed)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(ca.certificatePEM))
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: claudeAPIHost}); err != nil {
		t.Fatal(err)
	}
	if !original.Leaf.NotAfter.After(time.Now().Add(24 * time.Hour)) {
		t.Fatal("renewal mutated a certificate retained by an earlier handshake")
	}
}

func TestClaudeForwardCertificateRejectsUnannouncedCAReplacement(t *testing.T) {
	dir := claudeForwardPrivateTempDir(t)
	ca, err := openClaudeForwardCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &claudeForwarder{ca: ca}
	first, err := f.certificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	expiring := *first.Leaf
	expiring.NotAfter = time.Now().Add(time.Hour)
	f.certificates.leaf.Leaf = &expiring
	replacement, err := generateClaudeForwardCA()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, claudeForwardCAFile), replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.certificate(dir); err == nil {
		t.Fatal("renewal silently replaced the CA workers trust")
	}
	if f.ca.certificatePEM != ca.certificatePEM {
		t.Fatal("renewal changed advertised trust")
	}
}
