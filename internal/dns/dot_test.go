package dns_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dns-edge/internal/iface"
	"dns-edge/internal/testutil"
)

func pemBlock(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}

// generateSelfSignedCert builds a minimal in-memory self-signed certificate
// for "127.0.0.1", good enough to exercise a real TLS handshake in tests —
// no files on disk, no external dependency on openssl being installed.
func generateSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "dns-edge-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := tls.X509KeyPair(
		pemBlock("CERTIFICATE", der),
		pemBlock("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)),
	)
	require.NoError(t, err)
	return cert
}

// TestServeDNS_OverTLS exercises a real DoT (DNS over TLS) round trip end to
// end: a genuine mdns.Server bound to Net:"tcp-tls" with a self-signed cert,
// queried by a genuine mdns.Client over the same protocol — this is the
// scenario cmd/dns-edge/main.go wires up for real, just with a real
// self-signed cert standing in for the one edgeagent would otherwise push
// into the CertStore. No AXFR/DoH-specific code is involved: this is the
// "almost free" DoT path that reuses ServeDNS/handleQuery unmodified via
// miekg/dns's native tcp-tls support (server.go's "tcp-tls" case).
func TestServeDNS_OverTLS(t *testing.T) {
	rec := testutil.MakeA("www.example.com.", "1.2.3.4", 300, 0)
	store := &testutil.MockZoneStore{
		LookupFn: func(name string, qtype uint16) []*iface.Record {
			if name == "www.example.com." && qtype == mdns.TypeA {
				return []*iface.Record{rec}
			}
			return nil
		},
	}
	h := newHandler(store, &testutil.MockWeightProvider{})
	mux := mdns.NewServeMux()
	mux.Handle(".", h)

	cert := generateSelfSignedCert(t)

	started := make(chan struct{})
	srv := &mdns.Server{
		Net:               "tcp-tls",
		Addr:              "127.0.0.1:0",
		Handler:           mux,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}},
		NotifyStartedFunc: func() { close(started) },
	}
	go func() { _ = srv.ListenAndServe() }()
	defer srv.Shutdown() //nolint:errcheck

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("DoT server did not start in time")
	}
	addr := srv.Listener.Addr().String()

	client := &mdns.Client{Net: "tcp-tls", TLSConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // test-only self-signed cert
	m := makeQuery("www.example.com.", mdns.TypeA)
	resp, _, err := client.Exchange(m, addr)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, mdns.RcodeSuccess, resp.Rcode)
	require.Len(t, resp.Answer, 1)
	assert.True(t, resp.Authoritative)
}
