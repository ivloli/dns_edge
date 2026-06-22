package dns_test

import (
	"testing"

	mdns "github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	dnshandler "dns-edge/internal/dns"
	"dns-edge/internal/iface"
	"dns-edge/internal/testutil"
)

// newHandler is a convenience constructor for tests.
func newHandler(store iface.ZoneStore, weights iface.WeightProvider) *dnshandler.Handler {
	return dnshandler.NewHandler(store, weights, nil, 0, zap.NewNop())
}

func makeQuery(name string, qtype uint16) *mdns.Msg {
	m := new(mdns.Msg)
	m.SetQuestion(name, qtype)
	return m
}

// ── A record queries ──────────────────────────────────────────────────────────

func TestServeDNS_A_Hit(t *testing.T) {
	rec := testutil.MakeA("www.example.com.", "1.2.3.4", 300, 0)
	store := &testutil.MockZoneStore{
		LookupFn: func(name string, qtype uint16) []*iface.Record {
			if name == "www.example.com." && qtype == mdns.TypeA {
				return []*iface.Record{rec}
			}
			return nil
		},
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("www.example.com.", mdns.TypeA))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	assert.Len(t, m.Answer, 1)
	assert.True(t, m.Authoritative)
}

func TestServeDNS_A_WeightedPick(t *testing.T) {
	// Two records with equal weight — both must be reachable over many runs.
	r1 := testutil.MakeA("api.example.com.", "1.1.1.1", 10, 1)
	r2 := testutil.MakeA("api.example.com.", "2.2.2.2", 10, 1)
	store := &testutil.MockZoneStore{
		LookupFn: func(name string, qtype uint16) []*iface.Record {
			if name == "api.example.com." && qtype == mdns.TypeA {
				return []*iface.Record{r1, r2}
			}
			return nil
		},
	}
	h := newHandler(store, &testutil.MockWeightProvider{})
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		rw := testutil.NewFakeRW()
		h.ServeDNS(rw, makeQuery("api.example.com.", mdns.TypeA))
		m := rw.LastMsg()
		require.NotNil(t, m)
		require.Len(t, m.Answer, 1)
		seen[m.Answer[0].(*mdns.A).A.String()] = true
	}
	assert.True(t, seen["1.1.1.1"], "first record never selected")
	assert.True(t, seen["2.2.2.2"], "second record never selected")
}

// ── MX returns full rrset ─────────────────────────────────────────────────────

func TestServeDNS_MX_ReturnsAll(t *testing.T) {
	mx1 := testutil.MakeMX("example.com.", 10, "mail1.example.com.", 300)
	mx2 := testutil.MakeMX("example.com.", 20, "mail2.example.com.", 300)
	store := &testutil.MockZoneStore{
		LookupFn: func(name string, qtype uint16) []*iface.Record {
			if name == "example.com." && qtype == mdns.TypeMX {
				return []*iface.Record{mx1, mx2}
			}
			return nil
		},
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("example.com.", mdns.TypeMX))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	assert.Len(t, m.Answer, 2)
}

// ── NXDOMAIN ──────────────────────────────────────────────────────────────────

func TestServeDNS_NXDOMAIN(t *testing.T) {
	zone := &iface.Zone{Name: "example.com.", Records: map[iface.RecordKey][]*iface.Record{}}
	store := &testutil.MockZoneStore{
		LookupFn:     func(string, uint16) []*iface.Record { return nil },
		NameExistsFn: func(string) bool { return false },
		FindZoneFn:   func(string) *iface.Zone { return zone },
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("ghost.example.com.", mdns.TypeA))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeNameError, m.Rcode)
	assert.NotEmpty(t, m.Ns, "SOA should be in authority section")
}

// ── NODATA ────────────────────────────────────────────────────────────────────

func TestServeDNS_NODATA(t *testing.T) {
	zone := &iface.Zone{Name: "example.com.", Records: map[iface.RecordKey][]*iface.Record{}}
	store := &testutil.MockZoneStore{
		LookupFn:     func(string, uint16) []*iface.Record { return nil },
		NameExistsFn: func(string) bool { return true }, // name exists, wrong type
		FindZoneFn:   func(string) *iface.Zone { return zone },
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("www.example.com.", mdns.TypeAAAA))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	assert.Empty(t, m.Answer)
	assert.NotEmpty(t, m.Ns, "SOA should be in authority section")
}

// ── REFUSED (not authoritative) ───────────────────────────────────────────────

func TestServeDNS_Refused_NotAuthoritative(t *testing.T) {
	store := &testutil.MockZoneStore{
		LookupFn:   func(string, uint16) []*iface.Record { return nil },
		FindZoneFn: func(string) *iface.Zone { return nil },
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("www.other.com.", mdns.TypeA))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeRefused, m.Rcode)
}

// ── CNAME chasing ─────────────────────────────────────────────────────────────

func TestServeDNS_CNAME_Chase(t *testing.T) {
	cn := testutil.MakeCNAME("www.example.com.", "real.example.com.", 300)
	aRec := testutil.MakeA("real.example.com.", "1.2.3.4", 300, 0)

	store := &testutil.MockZoneStore{
		LookupFn: func(name string, qtype uint16) []*iface.Record {
			switch {
			case name == "www.example.com." && qtype == mdns.TypeA:
				return nil
			case name == "www.example.com." && qtype == mdns.TypeCNAME:
				return []*iface.Record{cn}
			case name == "real.example.com." && qtype == mdns.TypeA:
				return []*iface.Record{aRec}
			}
			return nil
		},
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("www.example.com.", mdns.TypeA))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	require.Len(t, m.Answer, 2, "CNAME + A")
	assert.Equal(t, mdns.TypeCNAME, m.Answer[0].Header().Rrtype)
	assert.Equal(t, mdns.TypeA, m.Answer[1].Header().Rrtype)
}

func TestServeDNS_CNAME_NoTargetRecords(t *testing.T) {
	cn := testutil.MakeCNAME("alias.example.com.", "real.example.com.", 300)
	store := &testutil.MockZoneStore{
		LookupFn: func(name string, qtype uint16) []*iface.Record {
			if name == "alias.example.com." && qtype == mdns.TypeCNAME {
				return []*iface.Record{cn}
			}
			return nil
		},
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("alias.example.com.", mdns.TypeA))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	// Only the CNAME itself; no A record
	assert.Len(t, m.Answer, 1)
	assert.Equal(t, mdns.TypeCNAME, m.Answer[0].Header().Rrtype)
}

// ── TypeANY → RFC 8482 ────────────────────────────────────────────────────────

func TestServeDNS_ANY_RFC8482(t *testing.T) {
	rw := testutil.NewFakeRW()
	newHandler(&testutil.MockZoneStore{}, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("www.example.com.", mdns.TypeANY))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	require.Len(t, m.Answer, 1)
	hinfo, ok := m.Answer[0].(*mdns.HINFO)
	require.True(t, ok, "answer should be HINFO")
	assert.Equal(t, "RFC8482", hinfo.Cpu)
}

// ── Empty question ────────────────────────────────────────────────────────────

func TestServeDNS_EmptyQuestion(t *testing.T) {
	rw := testutil.NewFakeRW()
	msg := new(mdns.Msg)
	msg.Id = 1
	newHandler(&testutil.MockZoneStore{}, &testutil.MockWeightProvider{}).ServeDNS(rw, msg)

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeFormatError, m.Rcode)
}

// ── AXFR ──────────────────────────────────────────────────────────────────────

func TestServeDNS_AXFR_TCP(t *testing.T) {
	aRec := testutil.MakeA("www.example.com.", "1.2.3.4", 300, 0)
	zone := testutil.MakeZone("example.com.", aRec)
	store := &testutil.MockZoneStore{
		FindZoneFn: func(string) *iface.Zone { return zone },
	}
	rw := testutil.NewFakeTCPRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("example.com.", mdns.TypeAXFR))

	// Minimum 3 messages: opening SOA + records + closing SOA
	require.GreaterOrEqual(t, len(rw.Msgs), 2)
	first := rw.Msgs[0]
	last := rw.Msgs[len(rw.Msgs)-1]
	require.Len(t, first.Answer, 1)
	require.Len(t, last.Answer, 1)
	assert.Equal(t, mdns.TypeSOA, first.Answer[0].Header().Rrtype, "first msg must be SOA")
	assert.Equal(t, mdns.TypeSOA, last.Answer[0].Header().Rrtype, "last msg must be SOA")
}

func TestServeDNS_AXFR_UDP_Refused(t *testing.T) {
	zone := testutil.MakeZone("example.com.")
	store := &testutil.MockZoneStore{
		FindZoneFn: func(string) *iface.Zone { return zone },
	}
	rw := testutil.NewFakeRW() // UDP
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("example.com.", mdns.TypeAXFR))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeRefused, m.Rcode)
}

func TestServeDNS_AXFR_ZoneNotFound(t *testing.T) {
	store := &testutil.MockZoneStore{
		FindZoneFn: func(string) *iface.Zone { return nil },
	}
	rw := testutil.NewFakeTCPRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("nozone.com.", mdns.TypeAXFR))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeRefused, m.Rcode)
}

// ── syntheticSOA ──────────────────────────────────────────────────────────────

func TestServeDNS_SyntheticSOA_InAuthority(t *testing.T) {
	// Zone with no stored SOA → handler must synthesise one for NXDOMAIN
	zone := &iface.Zone{Name: "example.com.", Records: map[iface.RecordKey][]*iface.Record{}}
	store := &testutil.MockZoneStore{
		LookupFn:     func(string, uint16) []*iface.Record { return nil },
		NameExistsFn: func(string) bool { return false },
		FindZoneFn:   func(string) *iface.Zone { return zone },
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("gone.example.com.", mdns.TypeA))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeNameError, m.Rcode)
	require.Len(t, m.Ns, 1)
	soa, ok := m.Ns[0].(*mdns.SOA)
	require.True(t, ok, "authority record must be SOA")
	assert.Equal(t, "example.com.", soa.Header().Name)
}

// ── Probabilistic sync trigger ────────────────────────────────────────────────

func TestServeDNS_ProbSync_Called(t *testing.T) {
	rec := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record { return []*iface.Record{rec} },
	}
	syncer := &testutil.MockSyncer{}
	// prob=1.0 → always trigger
	h := dnshandler.NewHandler(store, &testutil.MockWeightProvider{}, syncer, 1.0, zap.NewNop())
	rw := testutil.NewFakeRW()
	h.ServeDNS(rw, makeQuery("www.example.com.", mdns.TypeA))

	assert.Equal(t, 1, syncer.TriggerCount)
}
