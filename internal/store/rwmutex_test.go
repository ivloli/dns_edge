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
