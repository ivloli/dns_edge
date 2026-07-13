package dns

import (
	"crypto/tls"
	"errors"
	"sync"
)

// CertStore holds the currently active TLS certificate for a DoT/DoH
// listener, updated via copy-on-write (mirrors internal/store.SetSOA's
// pattern: replace the whole holder atomically, never mutate in place) so
// concurrent TLS handshakes never observe a half-updated certificate.
//
// TLS and DoH each get their own independent CertStore instance — the two
// protocols can reference different SSLPolicy configs on the edgeapi side
// (mirrors GoEdge's official "④TLS"/"⑤DoH" being two separate settings
// pages), so their certificates are never assumed to be the same.
type CertStore struct {
	mu  sync.RWMutex
	cur *tls.Certificate // nil = not configured / disabled
}

// NewCertStore returns an empty store (GetCertificate errors until Set is
// called with a valid cert).
func NewCertStore() *CertStore {
	return &CertStore{}
}

// Set parses certPEM/keyPEM and swaps them in atomically. Passing isOn=false
// (or empty PEM) clears the store back to "not configured", which makes
// GetCertificate fail closed — the TLS handshake itself fails rather than
// falling back to any previous certificate or a self-signed default.
func (s *CertStore) Set(certPEM, keyPEM []byte, isOn bool) error {
	if !isOn || len(certPEM) == 0 || len(keyPEM) == 0 {
		s.mu.Lock()
		s.cur = nil
		s.mu.Unlock()
		return nil
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.cur = &cert
	s.mu.Unlock()
	return nil
}

// GetCertificate is a tls.Config.GetCertificate callback. Returning an error
// here fails the TLS handshake at its earliest stage — the intended "off"
// behavior when the cluster's TLS/DoH setting is disabled or has never been
// successfully pushed down from edgeapi yet.
func (s *CertStore) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cur == nil {
		return nil, errors.New("dns-edge: TLS/DoH not configured for this cluster")
	}
	return s.cur, nil
}
