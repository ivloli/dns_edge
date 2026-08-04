package dns_test

import (
	"net"
	"testing"

	mdns "github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	dnshandler "dns-edge/internal/dns"
	"dns-edge/internal/geo"
	"dns-edge/internal/iface"
	"dns-edge/internal/testutil"
)

// newHandler is a convenience constructor for tests.
func newHandler(store iface.ZoneStore, weights iface.WeightProvider) *dnshandler.Handler {
	return dnshandler.NewHandler(store, weights, nil, 0, zap.NewNop(), nil)
}

func makeQuery(name string, qtype uint16) *mdns.Msg {
	m := new(mdns.Msg)
	m.SetQuestion(name, qtype)
	return m
}

// makeQueryWithECS builds a query with an EDNS0 ECS option carrying clientIP.
func makeQueryWithECS(name string, qtype uint16, clientIP net.IP) *mdns.Msg {
	m := makeQuery(name, qtype)
	o := new(mdns.OPT)
	o.Hdr.Name = "."
	o.Hdr.Rrtype = mdns.TypeOPT
	ecs := new(mdns.EDNS0_SUBNET)
	ecs.Code = mdns.EDNS0SUBNET
	if ip4 := clientIP.To4(); ip4 != nil {
		ecs.Family = 1
		ecs.SourceNetmask = 24
		ecs.Address = ip4
	} else {
		ecs.Family = 2
		ecs.SourceNetmask = 48
		ecs.Address = clientIP
	}
	o.Option = append(o.Option, ecs)
	m.Extra = append(m.Extra, o)
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
	first := rw.LastMsg()
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

// ── zone apex NS ("hosts" cluster setting) ────────────────────────────────────

func TestServeDNS_NS_AnsweredFromZone(t *testing.T) {
	zone := &iface.Zone{
		Name:    "example.com.",
		Records: map[iface.RecordKey][]*iface.Record{},
		NS: []*mdns.NS{
			{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeNS, Class: mdns.ClassINET, Ttl: 3600}, Ns: "ns1.provider.test."},
			{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeNS, Class: mdns.ClassINET, Ttl: 3600}, Ns: "ns2.provider.test."},
		},
	}
	store := &testutil.MockZoneStore{
		LookupFn:     func(string, uint16) []*iface.Record { return nil },
		NameExistsFn: func(string) bool { return true },
		FindZoneFn:   func(string) *iface.Zone { return zone },
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("example.com.", mdns.TypeNS))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	require.Len(t, m.Answer, 2)
	for _, rr := range m.Answer {
		ns, ok := rr.(*mdns.NS)
		require.True(t, ok, "answer must be NS record")
		assert.Equal(t, "example.com.", ns.Header().Name)
	}
}

func TestServeDNS_NS_EmptyWhenHostsUnconfigured(t *testing.T) {
	// No zone.NS at all (never configured "hosts") — must NOT synthesize a
	// fake nameserver like syntheticSOA does for SOA; a made-up "ns1.<apex>"
	// would point real resolvers at a hostname with no address record.
	zone := &iface.Zone{Name: "example.com.", Records: map[iface.RecordKey][]*iface.Record{}}
	store := &testutil.MockZoneStore{
		LookupFn:     func(string, uint16) []*iface.Record { return nil },
		NameExistsFn: func(string) bool { return true },
		FindZoneFn:   func(string) *iface.Zone { return zone },
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("example.com.", mdns.TypeNS))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode, "NODATA, not an error")
	assert.Empty(t, m.Answer)
}

func TestServeDNS_NS_CaseInsensitiveApexMatch(t *testing.T) {
	// Real-world resolvers (Google/Cloudflare public DNS included) randomize
	// query-name case ("0x20 encoding"). The store already normalizes to
	// lowercase internally, so zone.Name comes back lowercase regardless of
	// how it was inserted — this must still match a mixed-case query name.
	zone := &iface.Zone{
		Name:    "example.com.",
		Records: map[iface.RecordKey][]*iface.Record{},
		NS: []*mdns.NS{
			{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeNS, Class: mdns.ClassINET, Ttl: 3600}, Ns: "ns1.provider.test."},
		},
	}
	store := &testutil.MockZoneStore{
		LookupFn:     func(string, uint16) []*iface.Record { return nil },
		NameExistsFn: func(string) bool { return true },
		FindZoneFn:   func(string) *iface.Zone { return zone },
	}
	rw := testutil.NewFakeRW()
	newHandler(store, &testutil.MockWeightProvider{}).ServeDNS(rw, makeQuery("ExAmPlE.CoM.", mdns.TypeNS))

	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode)
	require.Len(t, m.Answer, 1, "mixed-case query must still match the (lowercase-normalized) zone apex")
}

// ── Probabilistic sync trigger ────────────────────────────────────────────────

func TestServeDNS_ProbSync_Called(t *testing.T) {
	rec := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record { return []*iface.Record{rec} },
	}
	syncer := &testutil.MockSyncer{}
	// prob=1.0 → always trigger
	h := dnshandler.NewHandler(store, &testutil.MockWeightProvider{}, syncer, 1.0, zap.NewNop(), nil)
	rw := testutil.NewFakeRW()
	h.ServeDNS(rw, makeQuery("www.example.com.", mdns.TypeA))

	assert.Equal(t, 1, syncer.TriggerCount)
}

// ── Geo-routing (Phase 13) ────────────────────────────────────────────────────

// fakeGeo is a GeoLookup stub that returns a fixed GeoInfo for any IP.
type fakeGeo struct {
	info geo.GeoInfo
}

func (f *fakeGeo) Lookup(_ net.IP) geo.GeoInfo { return f.info }

// mapGeo is a GeoLookup stub keyed by exact IP string, for tests that need
// to prove a *specific* IP value reached h.geo.Lookup (fakeGeo ignores its
// argument entirely, so it can't distinguish "the right IP got through"
// from "some IP got through").
type mapGeo map[string]geo.GeoInfo

func (m mapGeo) Lookup(ip net.IP) geo.GeoInfo { return m[ip.String()] }

func newGeoHandler(store iface.ZoneStore, g dnshandler.GeoLookup) *dnshandler.Handler {
	return dnshandler.NewHandler(store, &testutil.MockWeightProvider{}, nil, 0, zap.NewNop(), g)
}

func TestGeoRouting_SpecificRouteSelected(t *testing.T) {
	// Two records for www: one default (empty tags), one Shanghai telecom.
	recDefault := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recDefault.RouteTags = ""
	recShanghai := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recShanghai.RouteTags = "province=上海;isp=电信"

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recDefault, recShanghai}
		},
	}

	// Client is from Shanghai Telecom → should always get 2.2.2.2
	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}}
	h := newGeoHandler(store, g)

	for i := 0; i < 20; i++ {
		rw := testutil.NewFakeRW()
		// inject ECS clientIP so handleQuery passes clientIP != nil
		req := makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4"))
		h.ServeDNS(rw, req)
		require.Len(t, rw.LastMsg().Answer, 1)
		a := rw.LastMsg().Answer[0].(*mdns.A)
		assert.Equal(t, "2.2.2.2", a.A.String(), "expected Shanghai-specific record")
	}
}

func TestGeoRouting_PriorityBreaksTierTie(t *testing.T) {
	// Two records both land in the same "isp" tier (same isp=电信 tag,
	// different target IPs) — without priority this would be a 50/50
	// weighted-random pick. recHigh's route carries a higher RoutePriority,
	// so it alone should win every time.
	recLow := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recLow.RouteTags = "isp=电信"
	recLow.RoutePriority = 0

	recHigh := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recHigh.RouteTags = "isp=电信"
	recHigh.RoutePriority = 5

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recLow, recHigh}
		},
	}

	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "", ISP: "电信"}}
	h := newGeoHandler(store, g)

	for i := 0; i < 20; i++ {
		rw := testutil.NewFakeRW()
		req := makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4"))
		h.ServeDNS(rw, req)
		require.Len(t, rw.LastMsg().Answer, 1)
		a := rw.LastMsg().Answer[0].(*mdns.A)
		assert.Equal(t, "2.2.2.2", a.A.String(), "higher RoutePriority record should always win the tie")
	}
}

func TestGeoRouting_PriorityNeverOverridesTierSpecificity(t *testing.T) {
	// recCountry matches only the less-specific 1-tag tier, but carries a
	// very high priority. recProvinceISP matches the more-specific 2-tag
	// tier with no priority set at all. The 2-tag tier must still win —
	// priority only breaks ties *within* a tier, it never promotes a less
	// specific tier over a more specific one.
	recCountry := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recCountry.RouteTags = "country=中国"
	recCountry.RoutePriority = 100

	recProvinceISP := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recProvinceISP.RouteTags = "province=上海;isp=电信"
	recProvinceISP.RoutePriority = 0

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recCountry, recProvinceISP}
		},
	}

	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}}
	h := newGeoHandler(store, g)

	for i := 0; i < 20; i++ {
		rw := testutil.NewFakeRW()
		req := makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4"))
		h.ServeDNS(rw, req)
		require.Len(t, rw.LastMsg().Answer, 1)
		a := rw.LastMsg().Answer[0].(*mdns.A)
		assert.Equal(t, "2.2.2.2", a.A.String(), "2-tag match must beat 1-tag match regardless of priority")
	}
}

func TestGeoRouting_SingleTagDimensionsArePeers(t *testing.T) {
	// Since Round 3, specificity is purely "how many tags fully match" — a
	// lone country=中国 and a lone isp=电信 are equally specific (both 1 tag),
	// unlike the old tiered model where isp/province dimensions implicitly
	// outranked a plain country match. With equal specificity, RoutePriority
	// breaks the tie, same as same-dimension ties always have.
	recCountry := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recCountry.RouteTags = "country=中国"
	recCountry.RoutePriority = 5

	recISP := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recISP.RouteTags = "isp=电信"
	recISP.RoutePriority = 0

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recCountry, recISP}
		},
	}

	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "", ISP: "电信"}}
	h := newGeoHandler(store, g)

	for i := 0; i < 20; i++ {
		rw := testutil.NewFakeRW()
		req := makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4"))
		h.ServeDNS(rw, req)
		require.Len(t, rw.LastMsg().Answer, 1)
		a := rw.LastMsg().Answer[0].(*mdns.A)
		assert.Equal(t, "1.1.1.1", a.A.String(), "equal-specificity single-tag records: higher RoutePriority wins")
	}
}

func TestGeoRouting_EqualPriorityStillLoadBalances(t *testing.T) {
	// Two same-tier records with equal (zero) RoutePriority — the existing
	// weighted-random pick must still see both as candidates, i.e. priority
	// narrowing must not collapse ties down to a single arbitrary winner.
	recA := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recA.RouteTags = "isp=电信"
	recB := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recB.RouteTags = "isp=电信"

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recA, recB}
		},
	}

	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "", ISP: "电信"}}
	h := newGeoHandler(store, g)

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		rw := testutil.NewFakeRW()
		req := makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4"))
		h.ServeDNS(rw, req)
		require.Len(t, rw.LastMsg().Answer, 1)
		a := rw.LastMsg().Answer[0].(*mdns.A)
		seen[a.A.String()] = true
	}
	assert.Len(t, seen, 2, "both equal-priority records should still be reachable via weighted random")
}

func TestGeoRouting_FallbackToDefault(t *testing.T) {
	// Client is from overseas; no country=美国 record exists → fall back to default.
	recDefault := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recDefault.RouteTags = ""
	recShanghai := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recShanghai.RouteTags = "province=上海"

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recDefault, recShanghai}
		},
	}

	g := &fakeGeo{info: geo.GeoInfo{Country: "美国", Province: "", ISP: ""}}
	h := newGeoHandler(store, g)

	rw := testutil.NewFakeRW()
	h.ServeDNS(rw, makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("8.8.8.8")))
	require.Len(t, rw.LastMsg().Answer, 1)
	a := rw.LastMsg().Answer[0].(*mdns.A)
	assert.Equal(t, "1.1.1.1", a.A.String(), "expected default record for overseas client")
}

func TestGeoRouting_NoGeo_AllCandidates(t *testing.T) {
	// When geo is nil (no ECS or no xdb), pick from all records normally.
	rec1 := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 10)
	rec1.RouteTags = "province=上海"
	rec2 := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 10)
	rec2.RouteTags = ""

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{rec1, rec2}
		},
	}

	// nil geo, no ECS: handler uses all records
	h := newGeoHandler(store, nil)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		rw := testutil.NewFakeRW()
		h.ServeDNS(rw, makeQuery("www.example.com.", mdns.TypeA))
		if len(rw.LastMsg().Answer) > 0 {
			seen[rw.LastMsg().Answer[0].(*mdns.A).A.String()] = true
		}
	}
	// Both records should be selected over 50 rounds (equal weight=10)
	assert.True(t, seen["1.1.1.1"], "province record should appear without geo filter")
	assert.True(t, seen["2.2.2.2"], "default record should appear without geo filter")
}

func TestGeoRouting_NoECS_FallsBackToRemoteAddr(t *testing.T) {
	// No ECS in the query, but the geo router IS configured — clientIP
	// should fall back to the query's actual source address (handler.go's
	// remoteIP) rather than staying nil, so geo-routing still applies. Most
	// real resolvers never send ECS, so this fallback is what makes
	// geo-routing work for the common case, not just the +subnet= case.
	recDefault := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recDefault.RouteTags = ""
	recShanghai := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recShanghai.RouteTags = "province=上海"

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recDefault, recShanghai}
		},
	}

	// mapGeo only recognizes this exact IP — if the wrong IP (or none)
	// reached h.geo.Lookup, the lookup misses and returns the zero-value
	// GeoInfo, which matches nothing and falls through to the default record.
	remoteAddr := &net.UDPAddr{IP: net.ParseIP("9.8.7.6"), Port: 5353}
	g := mapGeo{"9.8.7.6": geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}}
	h := newGeoHandler(store, g)

	for i := 0; i < 20; i++ {
		rw := testutil.NewFakeRWWithAddr(remoteAddr)
		h.ServeDNS(rw, makeQuery("www.example.com.", mdns.TypeA))
		require.Len(t, rw.LastMsg().Answer, 1)
		a := rw.LastMsg().Answer[0].(*mdns.A)
		assert.Equal(t, "2.2.2.2", a.A.String(), "remote addr's geo should drive province routing even without ECS")
	}
}

func TestGeoRouting_ECS_TakesPriorityOverRemoteAddr(t *testing.T) {
	// When both an ECS option AND a remote address are available, ECS wins —
	// it's the resolver explicitly telling us the real client's subnet,
	// which is more trustworthy than the resolver's own connecting IP.
	recDefault := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recDefault.RouteTags = ""
	recShanghai := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recShanghai.RouteTags = "province=上海"

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recDefault, recShanghai}
		},
	}

	g := mapGeo{
		"1.2.3.4": geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}, // ECS address
		"9.8.7.6": geo.GeoInfo{Country: "中国", Province: "广东", ISP: "电信"}, // remote addr — different province, no matching record
	}
	h := newGeoHandler(store, g)

	remoteAddr := &net.UDPAddr{IP: net.ParseIP("9.8.7.6"), Port: 5353}
	rw := testutil.NewFakeRWWithAddr(remoteAddr)
	h.ServeDNS(rw, makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4")))
	require.Len(t, rw.LastMsg().Answer, 1)
	a := rw.LastMsg().Answer[0].(*mdns.A)
	assert.Equal(t, "2.2.2.2", a.A.String(), "ECS-supplied IP should be used, not the remote addr")
}

// ── Geo-routing fallback chain (province → ISP → country → default → all) ──

func TestGeoRouting_ProvinceISP_BothMatch(t *testing.T) {
	// Province+ISP record beats province-only, ISP-only, and default.
	recProvinceISP := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recProvinceISP.RouteTags = "province=上海;isp=电信"
	recProvince := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recProvince.RouteTags = "province=上海"
	recDefault := testutil.MakeA("www.example.com.", "3.3.3.3", 300, 0)
	recDefault.RouteTags = ""

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recProvinceISP, recProvince, recDefault}
		},
	}
	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}}
	h := newGeoHandler(store, g)

	for i := 0; i < 20; i++ {
		rw := testutil.NewFakeRW()
		h.ServeDNS(rw, makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4")))
		require.Len(t, rw.LastMsg().Answer, 1)
		assert.Equal(t, "1.1.1.1", rw.LastMsg().Answer[0].(*mdns.A).A.String(), "province+ISP record must win")
	}
}

func TestGeoRouting_FallbackToProvinceOnly(t *testing.T) {
	// No province+ISP record; should pick province-only over default.
	recProvince := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recProvince.RouteTags = "province=上海"
	recDefault := testutil.MakeA("www.example.com.", "3.3.3.3", 300, 0)
	recDefault.RouteTags = ""

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recProvince, recDefault}
		},
	}
	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}}
	h := newGeoHandler(store, g)

	rw := testutil.NewFakeRW()
	h.ServeDNS(rw, makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4")))
	require.Len(t, rw.LastMsg().Answer, 1)
	assert.Equal(t, "2.2.2.2", rw.LastMsg().Answer[0].(*mdns.A).A.String(), "province-only record must win over default")
}

func TestGeoRouting_FallbackToISPOnly(t *testing.T) {
	// No province match; should pick ISP-only over default.
	recISP := testutil.MakeA("www.example.com.", "4.4.4.4", 300, 0)
	recISP.RouteTags = "isp=电信"
	recDefault := testutil.MakeA("www.example.com.", "3.3.3.3", 300, 0)
	recDefault.RouteTags = ""

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recISP, recDefault}
		},
	}
	// Client is from Beijing Telecom (no province=北京 record, but isp=电信 exists)
	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "北京", ISP: "电信"}}
	h := newGeoHandler(store, g)

	rw := testutil.NewFakeRW()
	h.ServeDNS(rw, makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("2.2.2.2")))
	require.Len(t, rw.LastMsg().Answer, 1)
	assert.Equal(t, "4.4.4.4", rw.LastMsg().Answer[0].(*mdns.A).A.String(), "ISP-only record must win over default")
}

func TestGeoRouting_FallbackToCountry(t *testing.T) {
	// No province or ISP match; should pick country over default.
	recCountry := testutil.MakeA("www.example.com.", "5.5.5.5", 300, 0)
	recCountry.RouteTags = "country=中国"
	recDefault := testutil.MakeA("www.example.com.", "3.3.3.3", 300, 0)
	recDefault.RouteTags = ""

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recCountry, recDefault}
		},
	}
	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}}
	h := newGeoHandler(store, g)

	rw := testutil.NewFakeRW()
	h.ServeDNS(rw, makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4")))
	require.Len(t, rw.LastMsg().Answer, 1)
	assert.Equal(t, "5.5.5.5", rw.LastMsg().Answer[0].(*mdns.A).A.String(), "country record must win over default")
}

func TestGeoRouting_NoMatchAtAll_ReturnsNODATA(t *testing.T) {
	// Client is overseas; only province/ISP records exist, no default route.
	// The old model fell back to "all records" here — Round 3 removes that
	// fallback entirely: nothing fully matches, so the response must be a
	// standard NODATA (NOERROR, empty answer, SOA in authority), not a panic
	// and not an arbitrary record.
	recShanghai := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recShanghai.RouteTags = "province=上海"
	recTelecom := testutil.MakeA("www.example.com.", "2.2.2.2", 300, 0)
	recTelecom.RouteTags = "isp=电信"

	zone := &iface.Zone{Name: "example.com."}
	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recShanghai, recTelecom}
		},
		FindZoneFn: func(string) *iface.Zone { return zone },
	}
	g := &fakeGeo{info: geo.GeoInfo{Country: "美国", Province: "", ISP: ""}}
	h := newGeoHandler(store, g)

	rw := testutil.NewFakeRW()
	h.ServeDNS(rw, makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("8.8.8.8")))
	m := rw.LastMsg()
	require.NotNil(t, m)
	assert.Equal(t, mdns.RcodeSuccess, m.Rcode, "NODATA is NOERROR, not NXDOMAIN/SERVFAIL")
	assert.Empty(t, m.Answer, "no record should be returned once every candidate is excluded")
	require.Len(t, m.Ns, 1, "NODATA must carry exactly one SOA in the authority section")
	_, isSOA := m.Ns[0].(*mdns.SOA)
	assert.True(t, isSOA, "authority record must be SOA")
}

func TestGeoRouting_PartialTagMismatch_ExcludesRecordEntirely(t *testing.T) {
	// This is the exact bug the user reported: a record bound to
	// "country=中国;isp=电信" must NOT stay in the candidate pool just because
	// its country tag matches when the client's ISP doesn't (中国+移动 client
	// against a 中国+电信-only record). The old tiered model let the country
	// tag alone promote it into the "country" tier; the new model must
	// exclude it entirely, and fall through to the default route instead.
	recTelecom := testutil.MakeA("www.example.com.", "1.1.1.1", 300, 0)
	recTelecom.RouteTags = "country=中国;isp=电信"
	recDefault := testutil.MakeA("www.example.com.", "3.3.3.3", 300, 0)
	recDefault.RouteTags = ""

	store := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record {
			return []*iface.Record{recTelecom, recDefault}
		},
	}
	// Client is 中国+移动: country matches, isp does not.
	g := &fakeGeo{info: geo.GeoInfo{Country: "中国", Province: "", ISP: "移动"}}
	h := newGeoHandler(store, g)

	for i := 0; i < 20; i++ {
		rw := testutil.NewFakeRW()
		h.ServeDNS(rw, makeQueryWithECS("www.example.com.", mdns.TypeA, net.ParseIP("1.2.3.4")))
		require.Len(t, rw.LastMsg().Answer, 1)
		assert.Equal(t, "3.3.3.3", rw.LastMsg().Answer[0].(*mdns.A).A.String(),
			"country+isp record must be excluded entirely on ISP mismatch, not fall back into a country-only tier")
	}
}
