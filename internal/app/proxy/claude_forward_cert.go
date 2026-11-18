package proxy

import (
	"crypto/tls"
	"errors"
	"sync"
	"time"
)

// A persistent CA must not imply a 90-day maximum service uptime. Renew the
// short-lived leaf before expiry without changing the CA workers already trust.
// Certificate values returned to TLS are copies; later renewal never mutates
// certificates retained by an in-flight handshake.
type claudeForwardCertificates struct {
	mu   sync.Mutex
	leaf tls.Certificate
}

func (f *claudeForwarder) certificate(stateDir string) (*tls.Certificate, error) {
	f.certificates.mu.Lock()
	defer f.certificates.mu.Unlock()
	leaf := f.certificates.leaf
	if leaf.Leaf == nil {
		leaf = f.ca.leaf
	}
	if leaf.Leaf == nil || !time.Now().Add(24*time.Hour).Before(leaf.Leaf.NotAfter) {
		renewed, err := openClaudeForwardCA(stateDir)
		if err != nil || renewed.certificatePEM != f.ca.certificatePEM {
			return nil, errors.New("Claude proxy certificate renewal unavailable")
		}
		leaf = renewed.leaf
	}
	f.certificates.leaf = leaf
	return &leaf, nil
}
