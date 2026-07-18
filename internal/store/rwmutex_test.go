package store

import (
	"testing"

	mdns "github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dns-edge/internal/iface"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func makeA(t *testing.T, fqdn, ip string) *iface.Record {
	t.Helper()
	rr, err := mdns.NewRR(fqdn + " 300 IN A " + ip)
	require.NoError(t, err)
	return &iface.Record{Name: fqdn, Type: mdns.TypeA, TTL: 300, Value: ip, RR: rr}
}

func seedZone(t *testing.T, s *RWMutexStore, apex string, recs ...*iface.Record) {
	t.Helper()
	z := &iface.Zone{Name: apex, Records: make(map[iface.RecordKey][]*iface.Record)}
	for _, r := range recs {
		k := iface.RecordKey{Name: r.Name, Qtype: r.Type}
		z.Records[k] = append(z.Records[k], r)
	}
	require.NoError(t, s.Update(z))
}

// ── PutRecord ─────────────────────────────────────────────────────────────────

func TestPutRecord_Append(t *testing.T) {
	s := New()
	r1 := makeA(t, "www.example.com.", "1.2.3.4")
	r2 := makeA(t, "www.example.com.", "5.6.7.8")

	require.NoError(t, s.PutRecord("example.com.", r1))
	require.NoError(t, s.PutRecord("example.com.", r2))

	got := s.Lookup("www.example.com.", mdns.TypeA)
	assert.Len(t, got, 2)
}

func TestPutRecord_ReplaceByID(t *testing.T) {
	s := New()
	r1 := makeA(t, "www.example.com.", "1.2.3.4")
	r1.ID = 42

	require.NoError(t, s.PutRecord("example.com.", r1))

	r1updated := makeA(t, "www.example.com.", "9.9.9.9")
	r1updated.ID = 42
	require.NoError(t, s.PutRecord("example.com.", r1updated))

	got := s.Lookup("www.example.com.", mdns.TypeA)
	require.Len(t, got, 1)
	assert.Equal(t, "9.9.9.9", got[0].Value)
}

func TestPutRecord_CreatesZone(t *testing.T) {
	s := New()
	r := makeA(t, "api.new.com.", "1.1.1.1")

	require.NoError(t, s.PutRecord("new.com.", r))

	got := s.Lookup("api.new.com.", mdns.TypeA)
	require.Len(t, got, 1)
}

// TestPutRecord_EditingTypeRemovesStaleOldTypeEntry reproduces a real bug
// found live: a record originally created as NS (owner "ns1.timohoo.top",
// value "111.123.254.177" — an admin fat-fingered the type dropdown) and
// later corrected to A kept answering BOTH types forever, because
// PutRecord's replace-by-ID search only ever looked inside the *new* type's
// bucket. The database had one clean A row; dns-edge's memory still held a
// phantom NS record with the same ID under the old (name, NS) key.
func TestPutRecord_EditingTypeRemovesStaleOldTypeEntry(t *testing.T) {
	s := New()
	nsRR, err := mdns.NewRR("ns1.timohoo.top. 3600 IN NS 111.123.254.177.")
	require.NoError(t, err)
	original := &iface.Record{ID: 21, Name: "ns1.timohoo.top.", Type: mdns.TypeNS, Value: "111.123.254.177", RR: nsRR}
	require.NoError(t, s.PutRecord("timohoo.top.", original))

	// Admin edits the record's type from NS to A, same ID.
	edited := makeA(t, "ns1.timohoo.top.", "111.123.254.177")
	edited.ID = 21
	require.NoError(t, s.PutRecord("timohoo.top.", edited))

	assert.Empty(t, s.Lookup("ns1.timohoo.top.", mdns.TypeNS), "stale NS bucket must be cleared after the record's type changed to A")
	got := s.Lookup("ns1.timohoo.top.", mdns.TypeA)
	require.Len(t, got, 1)
	assert.Equal(t, "111.123.254.177", got[0].Value)
}

// TestPutRecord_RenamingRemovesStaleOldNameEntry is the same bug but for a
// renamed record (same ID, different owner name) instead of a retyped one.
func TestPutRecord_RenamingRemovesStaleOldNameEntry(t *testing.T) {
	s := New()
	r := makeA(t, "old-name.example.com.", "1.2.3.4")
	r.ID = 99
	require.NoError(t, s.PutRecord("example.com.", r))

	renamed := makeA(t, "new-name.example.com.", "1.2.3.4")
	renamed.ID = 99
	require.NoError(t, s.PutRecord("example.com.", renamed))

	assert.Empty(t, s.Lookup("old-name.example.com.", mdns.TypeA), "stale entry under the old name must be removed after a rename")
	assert.Len(t, s.Lookup("new-name.example.com.", mdns.TypeA), 1)
}

func TestPutRecord_COW_OldZoneUnchanged(t *testing.T) {
	s := New()
	r1 := makeA(t, "www.example.com.", "1.2.3.4")
	r1.ID = 1
	require.NoError(t, s.PutRecord("example.com.", r1))

	// capture zone pointer BEFORE second write
	zoneBefore := s.FindZone("www.example.com.")
	require.NotNil(t, zoneBefore)
	lenBefore := len(zoneBefore.Records[iface.RecordKey{Name: "www.example.com.", Qtype: mdns.TypeA}])

	r2 := makeA(t, "www.example.com.", "5.6.7.8")
	r2.ID = 2
	require.NoError(t, s.PutRecord("example.com.", r2))

	// old zone pointer must still reflect the state before the write
	assert.Len(t, zoneBefore.Records[iface.RecordKey{Name: "www.example.com.", Qtype: mdns.TypeA}], lenBefore)
	// new zone has the updated data
	assert.Len(t, s.Lookup("www.example.com.", mdns.TypeA), 2)
}

// ── DropRecord ────────────────────────────────────────────────────────────────

func TestDropRecord_RemovesRecord(t *testing.T) {
	s := New()
	r := makeA(t, "www.example.com.", "1.2.3.4")
	r.ID = 10
	require.NoError(t, s.PutRecord("example.com.", r))

	require.NoError(t, s.DropRecord("example.com.", 10))

	assert.Nil(t, s.Lookup("www.example.com.", mdns.TypeA))
}

func TestDropRecord_RemovesKeyWhenLast(t *testing.T) {
	s := New()
	r := makeA(t, "only.example.com.", "1.1.1.1")
	r.ID = 5
	require.NoError(t, s.PutRecord("example.com.", r))
	require.NoError(t, s.DropRecord("example.com.", 5))

	got := s.Lookup("only.example.com.", mdns.TypeA)
	assert.Nil(t, got)
}

func TestDropRecord_NoOp_WhenNotFound(t *testing.T) {
	s := New()
	// zone doesn't even exist
	assert.NoError(t, s.DropRecord("ghost.com.", 99))
}

func TestDropRecord_COW_OldZoneUnchanged(t *testing.T) {
	s := New()
	r := makeA(t, "www.example.com.", "1.2.3.4")
	r.ID = 7
	require.NoError(t, s.PutRecord("example.com.", r))

	zoneBefore := s.FindZone("www.example.com.")
	require.NotNil(t, zoneBefore)

	require.NoError(t, s.DropRecord("example.com.", 7))

	// old zone pointer still has the record
	key := iface.RecordKey{Name: "www.example.com.", Qtype: mdns.TypeA}
	assert.NotEmpty(t, zoneBefore.Records[key])
	// new lookup returns nothing
	assert.Nil(t, s.Lookup("www.example.com.", mdns.TypeA))
}

// ── NameExists ────────────────────────────────────────────────────────────────

func TestNameExists_True(t *testing.T) {
	s := New()
	r := makeA(t, "www.example.com.", "1.2.3.4")
	require.NoError(t, s.PutRecord("example.com.", r))
	assert.True(t, s.NameExists("www.example.com."))
}

func TestNameExists_FalseForUnknownName(t *testing.T) {
	s := New()
	r := makeA(t, "www.example.com.", "1.2.3.4")
	require.NoError(t, s.PutRecord("example.com.", r))
	assert.False(t, s.NameExists("other.example.com."))
}

func TestNameExists_FalseWhenNoZone(t *testing.T) {
	s := New()
	assert.False(t, s.NameExists("www.example.com."))
}

// ── FindZone ──────────────────────────────────────────────────────────────────

func TestFindZone_ExactApex(t *testing.T) {
	s := New()
	seedZone(t, s, "example.com.")
	z := s.FindZone("example.com.")
	require.NotNil(t, z)
	assert.Equal(t, "example.com.", z.Name)
}

func TestFindZone_Subdomain(t *testing.T) {
	s := New()
	seedZone(t, s, "example.com.")
	z := s.FindZone("api.v2.example.com.")
	require.NotNil(t, z)
	assert.Equal(t, "example.com.", z.Name)
}

func TestFindZone_NilForUnknown(t *testing.T) {
	s := New()
	seedZone(t, s, "example.com.")
	assert.Nil(t, s.FindZone("notexample.org."))
}

// ── Lookup ────────────────────────────────────────────────────────────────────

func TestLookup_ReturnsRRSet(t *testing.T) {
	s := New()
	r := makeA(t, "www.example.com.", "1.2.3.4")
	seedZone(t, s, "example.com.", r)
	got := s.Lookup("www.example.com.", mdns.TypeA)
	require.Len(t, got, 1)
	assert.Equal(t, r.Value, got[0].Value)
}

func TestLookup_NilForWrongType(t *testing.T) {
	s := New()
	r := makeA(t, "www.example.com.", "1.2.3.4")
	seedZone(t, s, "example.com.", r)
	assert.Nil(t, s.Lookup("www.example.com.", mdns.TypeAAAA))
}

// ── Snapshot ──────────────────────────────────────────────────────────────────

func TestSnapshot_ContainsAllZones(t *testing.T) {
	s := New()
	seedZone(t, s, "example.com.")
	seedZone(t, s, "other.net.")
	snap := s.Snapshot()
	assert.Contains(t, snap, "example.com.")
	assert.Contains(t, snap, "other.net.")
}

func TestSnapshot_IsShallowCopy(t *testing.T) {
	s := New()
	seedZone(t, s, "example.com.")
	snap := s.Snapshot()
	// deleting from snapshot does not affect the store
	delete(snap, "example.com.")
	assert.NotNil(t, s.FindZone("example.com."))
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestDelete_RemovesZone(t *testing.T) {
	s := New()
	seedZone(t, s, "example.com.")
	require.NoError(t, s.Delete("example.com."))
	assert.Nil(t, s.FindZone("example.com."))
}

func TestDelete_ErrorWhenNotFound(t *testing.T) {
	s := New()
	err := s.Delete("ghost.com.")
	assert.Error(t, err)
}

// ── SetNS ─────────────────────────────────────────────────────────────────────

func TestSetNS_UpdatesZone(t *testing.T) {
	s := New()
	seedZone(t, s, "example.com.")
	ns := []*mdns.NS{{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeNS}, Ns: "ns1.provider.test."}}

	require.NoError(t, s.SetNS("example.com.", ns))

	zone := s.FindZone("example.com.")
	require.NotNil(t, zone)
	assert.Equal(t, ns, zone.NS)
}

func TestSetNS_NoOp_WhenZoneAbsent(t *testing.T) {
	s := New()
	ns := []*mdns.NS{{Hdr: mdns.RR_Header{Name: "ghost.com.", Rrtype: mdns.TypeNS}, Ns: "ns1.provider.test."}}
	require.NoError(t, s.SetNS("ghost.com.", ns))
	assert.Nil(t, s.FindZone("ghost.com."))
}

// TestSetNS_PreservesSOAAndRecords guards against the exact bug class this
// copy-on-write pattern invites: adding a new Zone field (NS) means every
// existing constructor that rebuilds a Zone struct literal has to remember to
// carry it over, or a later write silently drops previously-set data. Here
// we assert the reverse direction: SetNS must not drop the SOA or Records
// that PutRecord/SetSOA already established.
func TestSetNS_PreservesSOAAndRecords(t *testing.T) {
	s := New()
	rec := makeA(t, "www.example.com.", "1.2.3.4")
	seedZone(t, s, "example.com.", rec)
	soa := &mdns.SOA{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeSOA}, Ns: "ns1.example.com."}
	require.NoError(t, s.SetSOA("example.com.", soa))

	ns := []*mdns.NS{{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeNS}, Ns: "ns1.provider.test."}}
	require.NoError(t, s.SetNS("example.com.", ns))

	zone := s.FindZone("example.com.")
	require.NotNil(t, zone)
	assert.Equal(t, ns, zone.NS)
	assert.Equal(t, soa, zone.SOA, "SetNS must not drop the previously-set SOA")
	assert.Len(t, zone.Records[iface.RecordKey{Name: "www.example.com.", Qtype: mdns.TypeA}], 1, "SetNS must not drop existing Records")
}

// TestSetSOA_PreservesNS is the mirror check: setting SOA after NS was
// already configured must not wipe the NS records back out.
func TestSetSOA_PreservesNS(t *testing.T) {
	s := New()
	seedZone(t, s, "example.com.")
	ns := []*mdns.NS{{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeNS}, Ns: "ns1.provider.test."}}
	require.NoError(t, s.SetNS("example.com.", ns))

	soa := &mdns.SOA{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeSOA}, Ns: "ns1.example.com."}
	require.NoError(t, s.SetSOA("example.com.", soa))

	zone := s.FindZone("example.com.")
	require.NotNil(t, zone)
	assert.Equal(t, ns, zone.NS, "SetSOA must not drop the previously-set NS")
}

// TestPutRecord_PreservesNS mirrors the existing SOA carry-over guarantee:
// PutRecord's copy-on-write must not drop a zone's NS records either.
func TestPutRecord_PreservesNS(t *testing.T) {
	s := New()
	seedZone(t, s, "example.com.")
	ns := []*mdns.NS{{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeNS}, Ns: "ns1.provider.test."}}
	require.NoError(t, s.SetNS("example.com.", ns))

	require.NoError(t, s.PutRecord("example.com.", makeA(t, "www.example.com.", "1.2.3.4")))

	zone := s.FindZone("example.com.")
	require.NotNil(t, zone)
	assert.Equal(t, ns, zone.NS, "PutRecord must not drop the previously-set NS")
}

// ── Case-insensitivity (RFC 1035 §4.1.4 / RFC 4343) ────────────────────────────
//
// Real-world resolvers (Google/Cloudflare public DNS included) commonly
// randomize query-name case ("0x20 encoding") as a cache-poisoning defense —
// every one of these must match regardless of what case the store's data was
// inserted under, or those resolvers get a spurious miss/REFUSED.

func TestPutRecord_LookupCaseInsensitive(t *testing.T) {
	s := New()
	require.NoError(t, s.PutRecord("Example.COM.", makeA(t, "WWW.Example.COM.", "1.2.3.4")))

	assert.Len(t, s.Lookup("www.example.com.", mdns.TypeA), 1, "lowercase lookup must find record stored under mixed case")
	assert.Len(t, s.Lookup("WWW.EXAMPLE.COM.", mdns.TypeA), 1, "uppercase lookup must find record stored under mixed case")
}

func TestFindZone_CaseInsensitive(t *testing.T) {
	s := New()
	seedZone(t, s, "Example.COM.")

	assert.NotNil(t, s.FindZone("example.com."), "lowercase FindZone must locate a zone stored under mixed case")
	assert.NotNil(t, s.FindZone("EXAMPLE.COM."), "uppercase FindZone must locate a zone stored under mixed case")
	assert.Equal(t, "example.com.", s.FindZone("EXAMPLE.COM.").Name, "stored zone name is normalized to lowercase")
}

func TestNameExists_CaseInsensitive(t *testing.T) {
	s := New()
	require.NoError(t, s.PutRecord("example.com.", makeA(t, "WWW.example.com.", "1.2.3.4")))
	assert.True(t, s.NameExists("www.EXAMPLE.com."))
}

func TestSetSOA_CaseInsensitive(t *testing.T) {
	s := New()
	seedZone(t, s, "EXAMPLE.com.")
	soa := &mdns.SOA{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeSOA}, Ns: "ns1.example.com."}
	require.NoError(t, s.SetSOA("example.COM.", soa))
	assert.Equal(t, soa, s.FindZone("Example.Com.").SOA)
}
