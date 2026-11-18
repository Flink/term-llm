package proxy

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func claudeForwardPrivateTempDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestClaudeForwardCAPersistenceAndTrust(t *testing.T) {
	dir := claudeForwardPrivateTempDir(t)
	first, err := openClaudeForwardCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := openClaudeForwardCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.certificatePEM != second.certificatePEM {
		t.Fatal("CA changed across restart")
	}
	if stringsContainsPrivateKey(first.certificatePEM) {
		t.Fatal("private key in public certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(first.certificatePEM)) {
		t.Fatal("invalid public PEM")
	}
	for _, host := range []string{claudeAPIHost, claudeOAuthHost, "other.example", "child." + claudeAPIHost, "child." + claudeOAuthHost} {
		_, err := first.leaf.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host})
		if (err == nil) != (host == claudeAPIHost || host == claudeOAuthHost) {
			t.Fatalf("verify %s: %v", host, err)
		}
	}
	// The root's critical DNS constraints also reject a rogue leaf that otherwise
	// has a matching SAN, rather than depending only on our leaf SAN selection.
	block, _ := pem.Decode([]byte(first.certificatePEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.PermittedDNSDomainsCritical || len(cert.PermittedDNSDomains) != 2 || cert.PermittedDNSDomains[0] != claudeAPIHost || cert.PermittedDNSDomains[1] != claudeOAuthHost {
		t.Fatal("missing narrow CA constraints", err)
	}
	info, err := os.Stat(filepath.Join(dir, claudeForwardCAFile))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("CA mode %o", info.Mode().Perm())
	}
}

func stringsContainsPrivateKey(s string) bool {
	return bytes.Contains([]byte(s), []byte("PRIVATE KEY"))
}

func TestClaudeForwardCAConcurrentPublication(t *testing.T) {
	dir := claudeForwardPrivateTempDir(t)
	const n = 8
	certificates := make(chan string, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ca, err := openClaudeForwardCA(dir)
			if err != nil {
				errs <- err
				return
			}
			certificates <- ca.certificatePEM
		}()
	}
	wg.Wait()
	close(errs)
	close(certificates)
	for err := range errs {
		t.Error(err)
	}
	var first string
	for cert := range certificates {
		if first != "" && cert != first {
			t.Fatal("initializers published different CAs")
		}
		first = cert
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary credentials leaked", entries, err)
	}
}

func TestClaudeForwardCARefusesUnsafeExistingState(t *testing.T) {
	for _, kind := range []string{"invalid", "symlink", "directory", "public-file", "public-directory", "symlink-directory"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && (kind == "public-file" || kind == "public-directory") {
				t.Skip("Unix mode contract")
			}
			dir := claudeForwardPrivateTempDir(t)
			path := filepath.Join(dir, claudeForwardCAFile)
			sentinel := []byte("never overwrite this private sentinel")
			target := filepath.Join(t.TempDir(), "untouched")
			if err := os.WriteFile(target, sentinel, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "invalid":
				if err := os.WriteFile(path, sentinel, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Skip(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.WriteFile(path, sentinel, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "public-directory":
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink-directory":
				link := filepath.Join(t.TempDir(), "linked-state")
				if err := os.Symlink(dir, link); err != nil {
					t.Skip(err)
				}
				dir = link
			}
			if _, err := openClaudeForwardCA(dir); err == nil {
				t.Fatal("accepted unsafe CA state")
			}
			data, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(data, sentinel) {
				t.Fatal("overwrote symlink target", err)
			}
			if kind == "invalid" || kind == "public-file" {
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(data, sentinel) {
					t.Fatal("overwrote existing CA", err)
				}
			}
		})
	}
}

func TestClaudeForwardCAConstrainsOtherIssuedNames(t *testing.T) {
	dir := claudeForwardPrivateTempDir(t)
	ca, err := openClaudeForwardCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, claudeForwardCAFile))
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(data, data)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(ca.certificatePEM))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{claudeAPIHost, claudeOAuthHost, "other.example", "child." + claudeAPIHost, "child." + claudeOAuthHost, "127.0.0.1", "::1"} {
		template := &x509.Certificate{SerialNumber: big.NewInt(42), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = []net.IP{ip}
		} else {
			template.DNSNames = []string{host}
		}
		der, err := x509.CreateCertificate(rand.Reader, template, root, key.Public(), pair.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		// SAN matches in every case. Only the critical root constraints explain
		// why non-Anthropic leaves fail, even if the CA key were reused elsewhere.
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host})
		if (err == nil) != (host == claudeAPIHost || host == claudeOAuthHost) {
			t.Fatalf("CA permitted %s: %v", host, err)
		}
	}
}

func TestClaudeForwardCARejectsMissingIPExclusions(t *testing.T) {
	data, err := generateClaudeForwardCA()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(data, data)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	_, private := pem.Decode(data)
	cert.ExcludedIPRanges = nil
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, cert.PublicKey, pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), private...)
	dir := claudeForwardPrivateTempDir(t)
	path := filepath.Join(dir, claudeForwardCAFile)
	if err := os.WriteFile(path, unsafe, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openClaudeForwardCA(dir); err == nil {
		t.Fatal("accepted a CA without IP name exclusions")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, unsafe) {
		t.Fatal("overwrote under-constrained CA instead of failing closed", err)
	}
}

func TestClaudeForwardCAV2DoesNotReuseLegacyInjectedIdentityTrust(t *testing.T) {
	dir := claudeForwardPrivateTempDir(t)
	legacy := filepath.Join(dir, "claude-forward-ca.pem")
	sentinel := []byte("legacy CA deliberately not reused or overwritten")
	if err := os.WriteFile(legacy, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	ca, err := openClaudeForwardCA(dir)
	if err != nil || ca == nil {
		t.Fatal("legacy trust blocked versioned vault trust", err)
	}
	after, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(after, sentinel) {
		t.Fatal("legacy trust overwritten", err)
	}
	if claudeForwardCAFile != "claude-forward-ca-v2.pem" {
		t.Fatal("native vault trust must remain versioned")
	}
}
