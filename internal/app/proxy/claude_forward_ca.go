package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

const claudeForwardCAFile = "claude-forward-ca-v2.pem"
const claudeOAuthHost = "platform.claude.com"
const claudeAPIHost = "api.anthropic.com"

type claudeForwardCA struct {
	certificatePEM string
	leaf           tls.Certificate
	vaultKey       [32]byte
}

// openClaudeForwardCA never replaces existing credentials, including malformed
// files or symlinks. A completed, synced private file is published using Link's
// no-replace semantics, so simultaneous initializers agree on one durable CA.
func openClaudeForwardCA(stateDir string) (*claudeForwardCA, error) {
	root, err := claudeForwardStateRoot(stateDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := readClaudeForwardCA(root)
	if errors.Is(err, os.ErrNotExist) {
		data, err = generateClaudeForwardCA()
		if err != nil {
			return nil, fmt.Errorf("generate Claude proxy CA: %w", err)
		}
		if err = publishClaudeForwardCA(root, data); err != nil {
			return nil, err
		}
		data, err = readClaudeForwardCA(root)
	}
	if err != nil {
		return nil, fmt.Errorf("read Claude proxy CA: %w", err)
	}
	return parseClaudeForwardCA(data)
}

func claudeForwardStateRoot(dir string) (*os.Root, error) {
	info, err := os.Lstat(filepath.Clean(dir))
	if err != nil {
		return nil, fmt.Errorf("inspect Claude proxy state: %w", err)
	}
	if !info.IsDir() || fileprivacy.CheckDir(info) != nil {
		return nil, fmt.Errorf("Claude proxy state must be a private non-symlink directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open Claude proxy state: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, fmt.Errorf("Claude proxy state changed while opening")
	}
	return root, nil
}

func readClaudeForwardCA(root *os.Root) ([]byte, error) {
	info, err := root.Lstat(claudeForwardCAFile)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || fileprivacy.CheckFile(info) != nil || info.Size() > 32<<10 {
		return nil, fmt.Errorf("Claude proxy CA must be a private regular file at most 32 KiB")
	}
	file, err := root.Open(claudeForwardCAFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || fileprivacy.CheckFile(opened) != nil {
		return nil, fmt.Errorf("Claude proxy CA changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, (32<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 32<<10 {
		return nil, fmt.Errorf("Claude proxy CA exceeds 32 KiB")
	}
	return data, nil
}

func publishClaudeForwardCA(root *os.Root, data []byte) error {
	name := ".claude-forward-ca-" + rand.Text()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileprivacy.FileMode)
	if err != nil {
		return fmt.Errorf("create Claude proxy CA: %w", err)
	}
	defer root.Remove(name)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("write Claude proxy CA: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close Claude proxy CA: %w", closeErr)
	}
	if err := root.Link(name, claudeForwardCAFile); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("publish Claude proxy CA: %w", err)
	}
	// Sync the same pinned directory, not a potentially renamed StateDir path.
	if runtime.GOOS != "windows" {
		dir, err := root.Open(".")
		if err != nil {
			return fmt.Errorf("open Claude proxy CA directory: %w", err)
		}
		defer dir.Close()
		if err := dir.Sync(); err != nil {
			return fmt.Errorf("sync Claude proxy CA directory: %w", err)
		}
	}
	return nil
}

func claudeForwardSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func generateClaudeForwardCA() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := claudeForwardSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	cert := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "term-llm Claude process proxy"},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true, PermittedDNSDomains: []string{claudeAPIHost, claudeOAuthHost},
		ExcludedDNSDomains: []string{"." + claudeAPIHost, "." + claudeOAuthHost},
		// DNS constraints alone do not constrain IP SANs. Exclude both IP
		// families so this process CA cannot also authorize literal IP hosts.
		ExcludedIPRanges: []*net.IPNet{
			{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
			{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		return nil, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return append(data, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})...), nil
}

func claudeForwardCAExcludesIPs(ca *x509.Certificate) bool {
	if len(ca.ExcludedIPRanges) != 2 {
		return false
	}
	var v4, v6 bool
	for _, subnet := range ca.ExcludedIPRanges {
		if subnet == nil {
			return false
		}
		ones, bits := subnet.Mask.Size()
		if ones != 0 {
			return false
		}
		switch bits {
		case 32:
			v4 = subnet.IP.Equal(net.IPv4zero)
		case 128:
			v6 = subnet.IP.Equal(net.IPv6zero)
		}
	}
	return v4 && v6
}

func parseClaudeForwardCA(data []byte) (*claudeForwardCA, error) {
	pair, err := tls.X509KeyPair(data, data)
	if err != nil || len(pair.Certificate) != 1 {
		return nil, fmt.Errorf("invalid Claude proxy CA credential")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse Claude proxy CA: %w", err)
	}
	now := time.Now()
	if !ca.IsCA || !ca.BasicConstraintsValid || !ca.MaxPathLenZero || ca.CheckSignatureFrom(ca) != nil ||
		!ca.PermittedDNSDomainsCritical || len(ca.PermittedDNSDomains) != 2 || ca.PermittedDNSDomains[0] != claudeAPIHost || ca.PermittedDNSDomains[1] != claudeOAuthHost ||
		len(ca.ExcludedDNSDomains) != 2 || ca.ExcludedDNSDomains[0] != "."+claudeAPIHost || ca.ExcludedDNSDomains[1] != "."+claudeOAuthHost || !claudeForwardCAExcludesIPs(ca) ||
		len(ca.ExtKeyUsage) != 1 || ca.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || now.Before(ca.NotBefore) || !now.Add(24*time.Hour).Before(ca.NotAfter) {
		return nil, fmt.Errorf("Claude proxy CA is expired or lacks its required scope constraints")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Claude proxy leaf key: %w", err)
	}
	serial, err := claudeForwardSerial()
	if err != nil {
		return nil, fmt.Errorf("generate Claude proxy leaf serial: %w", err)
	}
	until := now.AddDate(0, 0, 90)
	if ca.NotAfter.Before(until) {
		until = ca.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: claudeAPIHost}, DNSNames: []string{claudeAPIHost, claudeOAuthHost},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: until, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, key.Public(), pair.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign Claude proxy leaf: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse Claude proxy leaf: %w", err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("derive Claude vault key")
	}
	// Domain-separated from certificate signing and stable across leaf renewal.
	keyMaterial := append([]byte("term-llm/claude-vault/v2\x00"), private...)
	return &claudeForwardCA{
		vaultKey:       sha256.Sum256(keyMaterial),
		certificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})),
		leaf:           tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key, Leaf: leaf},
	}, nil
}
