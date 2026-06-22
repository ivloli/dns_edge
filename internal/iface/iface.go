package iface

import (
	"context"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// FQDN ensures name has a trailing dot.
func FQDN(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// ZoneMeta holds lightweight zone metadata returned by RecordStore.
type ZoneMeta struct {
	ID   int64
	Name string // apex FQDN with trailing dot
}

// Record is a single DNS resource record, optionally weighted for traffic splitting.
type Record struct {
	ID     int64  // PostgreSQL row ID; 0 for in-memory-only records
	Name   string // owner name as FQDN with trailing dot, e.g. "www.example.com."
	Type   uint16 // dns.TypeA, dns.TypeAAAA, dns.TypeCNAME, …
	TTL    uint32
	Value  string // text form of rdata, e.g. "1.2.3.4"
	Weight int    // static weight; 0 = equal distribution
	RR     dns.RR // pre-parsed resource record; populated when the record is stored
}

// Zone holds every record under a DNS zone apex.
type Zone struct {
	Name    string                  // apex FQDN with trailing dot, e.g. "example.com."
	Records map[RecordKey][]*Record // (owner name, qtype) → records
	SOA     *dns.SOA                // nil until Phase 2 (PostgreSQL load)
}

// RecordKey uniquely identifies a rrset within a zone.
type RecordKey struct {
	Name  string // FQDN with trailing dot
	Qtype uint16
}

// ZoneStore is the in-memory DNS record store used on every query hot path.
//
// Phase 1 implementation: RWMutexStore (sync.RWMutex).
// Phase 3 optional upgrade: COWStore (atomic.Value) — callers need not change.
type ZoneStore interface {
	// Lookup returns the rrset for (name, qtype), or nil if not found.
	Lookup(name string, qtype uint16) []*Record

	// Update atomically replaces (or inserts) an entire zone.
	Update(zone *Zone) error

	// Delete removes a zone by its apex FQDN.
	Delete(apex string) error

	// Snapshot returns a shallow copy of all zones, used by AXFR.
	Snapshot() map[string]*Zone

	// PutRecord adds or replaces a single record within the named zone.
	// Matches by rec.ID when ID > 0; appends if no match is found.
	// Creates an empty zone if apex is not yet in the store.
	PutRecord(apex string, rec *Record) error

	// DropRecord removes the record with the given ID from the named zone.
	// No-op if the zone or record is not in the store.
	DropRecord(apex string, id int64) error

	// NameExists reports whether name has any record of any type in the store.
	// Used to distinguish NXDOMAIN (name absent) from NODATA (no records of
	// the requested type).
	NameExists(name string) bool

	// FindZone returns the zone that is authoritative for name by walking the
	// label hierarchy. The returned pointer is safe to dereference after the
	// call returns because PutRecord/DropRecord use copy-on-write semantics.
	// Returns nil when no zone covers name.
	FindZone(name string) *Zone
}

// RecordStore is the PostgreSQL-backed persistence layer used by the HTTP API.
// All reads and writes flow through this interface; the ZoneStore is updated
// as a second step in the dual-write path.
type RecordStore interface {
	// Zone operations
	CreateZone(ctx context.Context, name string) (ZoneMeta, error)
	GetZone(ctx context.Context, apex string) (ZoneMeta, error)
	SoftDeleteZone(ctx context.Context, apex string) error
	ListZones(ctx context.Context) ([]ZoneMeta, error)

	// Record operations (zoneID is the PG primary-key from GetZone)
	CreateRecord(ctx context.Context, zoneID int64, rec *Record) (*Record, error)
	UpdateRecord(ctx context.Context, zoneID, id int64, rec *Record) (*Record, error)
	SoftDeleteRecord(ctx context.Context, zoneID, id int64) error
	ListRecords(ctx context.Context, apex string) ([]*Record, error)
}

// WeightProvider supplies dynamic per-domain traffic-splitting weights.
//
// Returns nil when no dynamic weights are available; the caller falls back
// to Record.Weight (static) and then to equal distribution.
//
// Phase 5 implementations:
//   - NacosWeightProvider  — primary; Nacos ListenConfig push, millisecond latency
//   - StaticWeightProvider — fallback; reads Record.Weight from ZoneStore
//   - CompositeWeightProvider — tries Nacos first, degrades to Static
type WeightProvider interface {
	// GetWeights returns a map of rdata-value → weight for (fqdn, qtype).
	// Returns nil if no dynamic weights are configured for this name.
	GetWeights(fqdn string, qtype uint16) map[string]int
}

// Syncer drives incremental pull from PostgreSQL into ZoneStore.
//
// Phase 4 implementation: polls every 30 s + 1 % probabilistic trigger with
// Token Bucket rate limiting.
type Syncer interface {
	TriggerSync() error
	Start(ctx context.Context)
}

// IncrementalLoader loads DNS records changed after a given point in time.
// Implemented by pg.Store; consumed by the syncer package.
type IncrementalLoader interface {
	IncrementalLoad(ctx context.Context, since time.Time, store ZoneStore) error
}
