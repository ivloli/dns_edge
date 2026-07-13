package dns_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mdns "github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dns-edge/internal/iface"
	"dns-edge/internal/testutil"
)

// packQuery builds and wire-encodes a simple query message, the same shape
// a real DoH client would send.
func packQuery(t *testing.T, name string, qtype uint16) []byte {
	t.Helper()
	m := makeQuery(name, qtype)
	wire, err := m.Pack()
	require.NoError(t, err)
	return wire
}

func doGET(t *testing.T, wire []byte) *http.Request {
	t.Helper()
	encoded := base64.RawURLEncoding.EncodeToString(wire)
	return httptest.NewRequest(http.MethodGet, "/dns-query?dns="+encoded, nil)
}

func doPOST(t *testing.T, wire []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/dns-query", strings.NewReader(string(wire)))
	req.Header.Set("Content-Type", "application/dns-message")
	return req
}

// unpackResponse asserts the recorder got a 200 with the DoH content type,
// and returns the decoded DNS message.
func unpackResponse(t *testing.T, rec *httptest.ResponseRecorder) *mdns.Msg {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/dns-message", rec.Header().Get("Content-Type"))
	m := new(mdns.Msg)
	require.NoError(t, m.Unpack(rec.Body.Bytes()))
	return m
}

func TestServeDoH_GET_A_Hit(t *testing.T) {
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

	req := doGET(t, packQuery(t, "www.example.com.", mdns.TypeA))
	w := httptest.NewRecorder()
	h.ServeDoH(w, req)

	m := unpackResponse(t, w)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	require.Len(t, m.Answer, 1)
	assert.True(t, m.Authoritative)
}

func TestServeDoH_POST_A_Hit(t *testing.T) {
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

	req := doPOST(t, packQuery(t, "www.example.com.", mdns.TypeA))
	w := httptest.NewRecorder()
	h.ServeDoH(w, req)

	m := unpackResponse(t, w)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	require.Len(t, m.Answer, 1)
	assert.True(t, m.Authoritative)
}

func TestServeDoH_NXDOMAIN(t *testing.T) {
	zone := &iface.Zone{Name: "example.com.", Records: map[iface.RecordKey][]*iface.Record{}}
	store := &testutil.MockZoneStore{
		LookupFn:     func(string, uint16) []*iface.Record { return nil },
		NameExistsFn: func(string) bool { return false },
		FindZoneFn:   func(string) *iface.Zone { return zone },
	}
	h := newHandler(store, &testutil.MockWeightProvider{})

	req := doPOST(t, packQuery(t, "ghost.example.com.", mdns.TypeA))
	w := httptest.NewRecorder()
	h.ServeDoH(w, req)

	m := unpackResponse(t, w)
	assert.Equal(t, mdns.RcodeNameError, m.Rcode)
}

func TestServeDoH_MethodNotAllowed(t *testing.T) {
	h := newHandler(&testutil.MockZoneStore{}, &testutil.MockWeightProvider{})

	req := httptest.NewRequest(http.MethodPut, "/dns-query", nil)
	w := httptest.NewRecorder()
	h.ServeDoH(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestServeDoH_GET_MissingDNSParam(t *testing.T) {
	h := newHandler(&testutil.MockZoneStore{}, &testutil.MockWeightProvider{})

	req := httptest.NewRequest(http.MethodGet, "/dns-query", nil)
	w := httptest.NewRecorder()
	h.ServeDoH(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestServeDoH_GET_InvalidBase64(t *testing.T) {
	h := newHandler(&testutil.MockZoneStore{}, &testutil.MockWeightProvider{})

	req := httptest.NewRequest(http.MethodGet, "/dns-query?dns=not-valid-base64!!!", nil)
	w := httptest.NewRecorder()
	h.ServeDoH(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestServeDoH_POST_MalformedMessage(t *testing.T) {
	h := newHandler(&testutil.MockZoneStore{}, &testutil.MockWeightProvider{})

	req := doPOST(t, []byte("not a dns message"))
	w := httptest.NewRecorder()
	h.ServeDoH(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestServeDoH_AXFR_NotImplemented(t *testing.T) {
	h := newHandler(&testutil.MockZoneStore{}, &testutil.MockWeightProvider{})

	req := doPOST(t, packQuery(t, "example.com.", mdns.TypeAXFR))
	w := httptest.NewRecorder()
	h.ServeDoH(w, req)

	assert.Equal(t, http.StatusNotImplemented, w.Code)
}
