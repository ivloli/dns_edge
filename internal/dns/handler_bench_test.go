package dns_test

// Benchmarks for the DNS query handler hot path.
// Using MockZoneStore (zero lock overhead) to isolate handler logic,
// plus a RealStore variant to measure the full ZoneStore cost.

import (
	"testing"

	mdns "github.com/miekg/dns"
	"go.uber.org/zap"

	dnshandler "dns-edge/internal/dns"
	"dns-edge/internal/iface"
	"dns-edge/internal/store"
	"dns-edge/internal/testutil"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func benchHandler(store iface.ZoneStore) *dnshandler.Handler {
	return dnshandler.NewHandler(store, &testutil.MockWeightProvider{}, nil, 0, zap.NewNop())
}

func newQuery(name string, qtype uint16) *mdns.Msg {
	m := new(mdns.Msg)
	m.SetQuestion(name, qtype)
	return m
}

// realStore returns a real RWMutexStore pre-loaded with test data.
func realStore(b *testing.B) iface.ZoneStore {
	b.Helper()
	s := store.New()
	for i := 0; i < 10; i++ {
		r := testutil.MakeA("www.example.com.", "1.2.3.4", 300, 0)
		_ = s.PutRecord("example.com.", r)
	}
	_ = s.PutRecord("example.com.", testutil.MakeCNAME("alias.example.com.", "www.example.com.", 300))
	return s
}

// ── A query with mock store (isolates handler + pick overhead) ────────────────

func BenchmarkServeDNS_A_MockStore(b *testing.B) {
	rec := testutil.MakeA("www.example.com.", "1.2.3.4", 300, 0)
	ms := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record { return []*iface.Record{rec} },
	}
	h := benchHandler(ms)
	rw := testutil.NewNullRW()
	req := newQuery("www.example.com.", mdns.TypeA)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.ServeDNS(rw, req)
	}
}

// ── A query with real RWMutexStore (full path including lock) ─────────────────

func BenchmarkServeDNS_A_RealStore(b *testing.B) {
	s := realStore(b)
	h := benchHandler(s)
	rw := testutil.NewNullRW()
	req := newQuery("www.example.com.", mdns.TypeA)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.ServeDNS(rw, req)
	}
}

// ── NXDOMAIN path ─────────────────────────────────────────────────────────────

func BenchmarkServeDNS_NXDOMAIN(b *testing.B) {
	zone := &iface.Zone{Name: "example.com.", Records: map[iface.RecordKey][]*iface.Record{}}
	ms := &testutil.MockZoneStore{
		LookupFn:     func(string, uint16) []*iface.Record { return nil },
		NameExistsFn: func(string) bool { return false },
		FindZoneFn:   func(string) *iface.Zone { return zone },
	}
	h := benchHandler(ms)
	rw := testutil.NewNullRW()
	req := newQuery("ghost.example.com.", mdns.TypeA)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.ServeDNS(rw, req)
	}
}

// ── CNAME chasing ─────────────────────────────────────────────────────────────

func BenchmarkServeDNS_CNAME_Chase(b *testing.B) {
	cn := testutil.MakeCNAME("alias.example.com.", "real.example.com.", 300)
	ar := testutil.MakeA("real.example.com.", "1.2.3.4", 300, 0)
	ms := &testutil.MockZoneStore{
		LookupFn: func(name string, qtype uint16) []*iface.Record {
			switch {
			case name == "alias.example.com." && qtype == mdns.TypeCNAME:
				return []*iface.Record{cn}
			case name == "real.example.com." && qtype == mdns.TypeA:
				return []*iface.Record{ar}
			}
			return nil
		},
	}
	h := benchHandler(ms)
	rw := testutil.NewNullRW()
	req := newQuery("alias.example.com.", mdns.TypeA)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.ServeDNS(rw, req)
	}
}

// ── TypeANY → RFC 8482 ────────────────────────────────────────────────────────

func BenchmarkServeDNS_ANY(b *testing.B) {
	h := benchHandler(&testutil.MockZoneStore{})
	rw := testutil.NewNullRW()
	req := newQuery("www.example.com.", mdns.TypeANY)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.ServeDNS(rw, req)
	}
}

// ── weighted pick with 2 A records ───────────────────────────────────────────

func BenchmarkServeDNS_A_WeightedPick(b *testing.B) {
	r1 := testutil.MakeA("api.example.com.", "1.1.1.1", 10, 70)
	r2 := testutil.MakeA("api.example.com.", "2.2.2.2", 10, 30)
	ms := &testutil.MockZoneStore{
		LookupFn: func(string, uint16) []*iface.Record { return []*iface.Record{r1, r2} },
	}
	h := benchHandler(ms)
	rw := testutil.NewNullRW()
	req := newQuery("api.example.com.", mdns.TypeA)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.ServeDNS(rw, req)
	}
}

// ── parallel queries against real store (simulates multi-core load) ───────────

func BenchmarkServeDNS_A_Parallel(b *testing.B) {
	s := realStore(b)
	h := benchHandler(s)
	req := newQuery("www.example.com.", mdns.TypeA)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		rw := testutil.NewNullRW()
		for pb.Next() {
			h.ServeDNS(rw, req)
		}
	})
}

// ── AXFR (TCP) ────────────────────────────────────────────────────────────────

func BenchmarkServeDNS_AXFR_TCP(b *testing.B) {
	s := realStore(b)
	h := benchHandler(s)
	rw := testutil.NewNullTCPRW()
	req := newQuery("example.com.", mdns.TypeAXFR)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.ServeDNS(rw, req)
	}
}
