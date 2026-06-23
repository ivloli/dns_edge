// Package testutil provides mock implementations and test helpers shared
// across test packages.
package testutil

import (
	"context"
	"net"
	"time"

	mdns "github.com/miekg/dns"

	"dns-edge/internal/iface"
)

// ── MockZoneStore ─────────────────────────────────────────────────────────────

// MockZoneStore is a function-field based mock of iface.ZoneStore.
// Unset fields return zero values silently.
type MockZoneStore struct {
	LookupFn     func(name string, qtype uint16) []*iface.Record
	UpdateFn     func(zone *iface.Zone) error
	DeleteFn     func(apex string) error
	SnapshotFn   func() map[string]*iface.Zone
	PutRecordFn  func(apex string, rec *iface.Record) error
	DropRecordFn func(apex string, id int64) error
	NameExistsFn func(name string) bool
	FindZoneFn   func(name string) *iface.Zone
}

var _ iface.ZoneStore = (*MockZoneStore)(nil)

func (m *MockZoneStore) Lookup(name string, qtype uint16) []*iface.Record {
	if m.LookupFn != nil {
		return m.LookupFn(name, qtype)
	}
	return nil
}
func (m *MockZoneStore) Update(zone *iface.Zone) error {
	if m.UpdateFn != nil {
		return m.UpdateFn(zone)
	}
	return nil
}
func (m *MockZoneStore) Delete(apex string) error {
	if m.DeleteFn != nil {
		return m.DeleteFn(apex)
	}
	return nil
}
func (m *MockZoneStore) Snapshot() map[string]*iface.Zone {
	if m.SnapshotFn != nil {
		return m.SnapshotFn()
	}
	return nil
}
func (m *MockZoneStore) PutRecord(apex string, rec *iface.Record) error {
	if m.PutRecordFn != nil {
		return m.PutRecordFn(apex, rec)
	}
	return nil
}
func (m *MockZoneStore) DropRecord(apex string, id int64) error {
	if m.DropRecordFn != nil {
		return m.DropRecordFn(apex, id)
	}
	return nil
}
func (m *MockZoneStore) NameExists(name string) bool {
	if m.NameExistsFn != nil {
		return m.NameExistsFn(name)
	}
	return false
}
func (m *MockZoneStore) FindZone(name string) *iface.Zone {
	if m.FindZoneFn != nil {
		return m.FindZoneFn(name)
	}
	return nil
}

// ── MockWeightProvider ────────────────────────────────────────────────────────

// MockWeightProvider is a function-field mock of iface.WeightProvider.
type MockWeightProvider struct {
	GetWeightsFn func(fqdn string, qtype uint16, clientIP net.IP) map[string]int
}

var _ iface.WeightProvider = (*MockWeightProvider)(nil)

func (m *MockWeightProvider) GetWeights(fqdn string, qtype uint16, clientIP net.IP) map[string]int {
	if m.GetWeightsFn != nil {
		return m.GetWeightsFn(fqdn, qtype, clientIP)
	}
	return nil
}

// ── MockSyncer ────────────────────────────────────────────────────────────────

// MockSyncer counts TriggerSync calls.
type MockSyncer struct {
	TriggerCount int
}

var _ iface.Syncer = (*MockSyncer)(nil)

func (m *MockSyncer) TriggerSync() error {
	m.TriggerCount++
	return nil
}
func (m *MockSyncer) Start(_ context.Context) {}

// ── MockIncrementalLoader ─────────────────────────────────────────────────────

// MockIncrementalLoader records calls to IncrementalLoad.
type MockIncrementalLoader struct {
	Fn        func(ctx context.Context, since time.Time, store iface.ZoneStore) error
	CallCount int
	LastSince time.Time
}

var _ iface.IncrementalLoader = (*MockIncrementalLoader)(nil)

func (m *MockIncrementalLoader) IncrementalLoad(ctx context.Context, since time.Time, store iface.ZoneStore) error {
	m.CallCount++
	m.LastSince = since
	if m.Fn != nil {
		return m.Fn(ctx, since, store)
	}
	return nil
}

// ── FakeResponseWriter ────────────────────────────────────────────────────────

// FakeRW is a fake mdns.ResponseWriter that captures every WriteMsg call.
// Use NewFakeRW (UDP) or NewFakeTCPRW (TCP) to control what RemoteAddr returns.
type FakeRW struct {
	Msgs []* mdns.Msg
	addr net.Addr
}

var _ mdns.ResponseWriter = (*FakeRW)(nil)

// NewFakeRW returns a UDP-addressed fake writer.
func NewFakeRW() *FakeRW {
	return &FakeRW{addr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}}
}

// NewFakeTCPRW returns a TCP-addressed fake writer (needed for AXFR tests).
func NewFakeTCPRW() *FakeRW {
	return &FakeRW{addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}}
}

func (f *FakeRW) WriteMsg(m *mdns.Msg) error {
	cp := m.Copy()
	f.Msgs = append(f.Msgs, cp)
	return nil
}
func (f *FakeRW) LocalAddr() net.Addr      { return f.addr }
func (f *FakeRW) RemoteAddr() net.Addr     { return f.addr }
func (f *FakeRW) Write(b []byte) (int, error) { return len(b), nil }
func (f *FakeRW) Close() error             { return nil }
func (f *FakeRW) TsigStatus() error        { return nil }
func (f *FakeRW) TsigTimersOnly(bool)      {}
func (f *FakeRW) Hijack()                  {}

// LastMsg returns the last message written, or nil if no messages were sent.
func (f *FakeRW) LastMsg() *mdns.Msg {
	if len(f.Msgs) == 0 {
		return nil
	}
	return f.Msgs[len(f.Msgs)-1]
}

// ── NullRW ────────────────────────────────────────────────────────────────────

// NullRW is a no-op ResponseWriter for benchmarks; it discards every message.
type NullRW struct{ addr net.Addr }

var _ mdns.ResponseWriter = (*NullRW)(nil)

// NewNullRW returns a UDP NullRW.
func NewNullRW() *NullRW {
	return &NullRW{addr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}}
}

// NewNullTCPRW returns a TCP NullRW (needed for AXFR benchmarks).
func NewNullTCPRW() *NullRW {
	return &NullRW{addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}}
}

func (n *NullRW) WriteMsg(*mdns.Msg) error     { return nil }
func (n *NullRW) Write(b []byte) (int, error)  { return len(b), nil }
func (n *NullRW) LocalAddr() net.Addr          { return n.addr }
func (n *NullRW) RemoteAddr() net.Addr         { return n.addr }
func (n *NullRW) Close() error                 { return nil }
func (n *NullRW) TsigStatus() error            { return nil }
func (n *NullRW) TsigTimersOnly(bool)          {}
func (n *NullRW) Hijack()                      {}

// ── Record helpers ────────────────────────────────────────────────────────────

// MakeA builds an iface.Record for an A record.
func MakeA(fqdn, ip string, ttl uint32, weight int) *iface.Record {
	rr, _ := mdns.NewRR(fqdn + " " + itoa(int(ttl)) + " IN A " + ip)
	return &iface.Record{Name: fqdn, Type: mdns.TypeA, TTL: ttl, Value: ip, Weight: weight, RR: rr}
}

// MakeCNAME builds an iface.Record for a CNAME record.
func MakeCNAME(owner, target string, ttl uint32) *iface.Record {
	rr, _ := mdns.NewRR(owner + " " + itoa(int(ttl)) + " IN CNAME " + target)
	return &iface.Record{Name: owner, Type: mdns.TypeCNAME, TTL: ttl, Value: target, RR: rr}
}

// MakeMX builds an iface.Record for an MX record.
func MakeMX(owner string, pref uint16, exchange string, ttl uint32) *iface.Record {
	rr, _ := mdns.NewRR(owner + " " + itoa(int(ttl)) + " IN MX " + itoa(int(pref)) + " " + exchange)
	return &iface.Record{Name: owner, Type: mdns.TypeMX, TTL: ttl, Value: exchange, RR: rr}
}

// MakeZone builds a minimal zone with the given records.
func MakeZone(apex string, recs ...*iface.Record) *iface.Zone {
	m := make(map[iface.RecordKey][]*iface.Record)
	for _, r := range recs {
		k := iface.RecordKey{Name: r.Name, Qtype: r.Type}
		m[k] = append(m[k], r)
	}
	return &iface.Zone{Name: apex, Records: m}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
