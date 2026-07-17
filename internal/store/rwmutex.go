package store

import (
	"fmt"
	"strings"
	"sync"

	"github.com/miekg/dns"

	"dns-edge/internal/iface"
)

// RWMutexStore is the Phase-1 ZoneStore backed by sync.RWMutex.
//
// Reads share a read lock; writes take an exclusive lock.
// PutRecord and DropRecord use copy-on-write semantics: every write publishes
// a brand-new Zone value, making the previously-published Zone objects safe to
// read without a lock (AXFR, CNAME chasing, etc.).
type RWMutexStore struct {
	mu    sync.RWMutex
	zones map[string]*iface.Zone // key: zone apex FQDN, e.g. "example.com."
}

// Compile-time interface check.
var _ iface.ZoneStore = (*RWMutexStore)(nil)

// New returns an empty RWMutexStore.
func New() *RWMutexStore {
	return &RWMutexStore{zones: make(map[string]*iface.Zone)}
}

// normalizeName lowercases a DNS name for use as a map key. DNS names are
// case-insensitive (RFC 1035 §4.1.4, RFC 4343) — real-world resolvers
// (Google/Cloudflare public DNS included) commonly randomize query-name case
// ("0x20 encoding") as a spoofing defense, so every zones/Records map key
// must be compared case-insensitively or those resolvers get REFUSED for
// every single query. Applied uniformly at both the read and write paths
// below so it doesn't matter which case the caller (query dispatch vs.
// admin-entered domain/record names) happens to use.
func normalizeName(name string) string {
	return strings.ToLower(name)
}

// Lookup returns the rrset for (name, qtype), walking up the label hierarchy
// to find the authoritative zone. Returns nil if not found.
func (s *RWMutexStore) Lookup(name string, qtype uint16) []*iface.Record {
	name = normalizeName(name)
	s.mu.RLock()
	defer s.mu.RUnlock()

	zone := s.lockedFindZone(name)
	if zone == nil {
		return nil
	}
	return zone.Records[iface.RecordKey{Name: name, Qtype: qtype}]
}

// FindZone returns the zone authoritative for name. The returned pointer is
// safe to dereference after the call returns because PutRecord/DropRecord use
// copy-on-write semantics. Returns nil when no zone covers name.
func (s *RWMutexStore) FindZone(name string) *iface.Zone {
	name = normalizeName(name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lockedFindZone(name)
}

// NameExists reports whether name has any record of any type in the store.
// Used to distinguish NXDOMAIN (name absent) from NODATA (no records of the
// requested type).
func (s *RWMutexStore) NameExists(name string) bool {
	name = normalizeName(name)
	s.mu.RLock()
	defer s.mu.RUnlock()

	zone := s.lockedFindZone(name)
	if zone == nil {
		return false
	}
	for k := range zone.Records {
		if k.Name == name {
			return true
		}
	}
	return false
}

// lockedFindZone walks up the DNS label hierarchy to find the zone that owns
// name. Must be called with at least a read lock held. name must already be
// normalized (callers above all do this).
func (s *RWMutexStore) lockedFindZone(name string) *iface.Zone {
	n := name
	for {
		if z, ok := s.zones[n]; ok {
			return z
		}
		dot := strings.IndexByte(n, '.')
		if dot < 0 || dot == len(n)-1 {
			break
		}
		n = n[dot+1:]
	}
	return nil
}

// Update atomically replaces (or inserts) the entire zone. zone.Name is
// normalized in place so the stored value's case matches every other write
// path (PutRecord/SetSOA/SetNS all construct their Zone with an already-
// normalized apex) — callers must not assume their original zone.Name
// survives unchanged.
func (s *RWMutexStore) Update(zone *iface.Zone) error {
	zone.Name = normalizeName(zone.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.zones[zone.Name] = zone
	return nil
}

// Delete removes the zone with the given apex FQDN.
func (s *RWMutexStore) Delete(apex string) error {
	apex = normalizeName(apex)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.zones[apex]; !ok {
		return fmt.Errorf("zone %q not found", apex)
	}
	delete(s.zones, apex)
	return nil
}

// Snapshot returns a shallow copy of all zones.
func (s *RWMutexStore) Snapshot() map[string]*iface.Zone {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := make(map[string]*iface.Zone, len(s.zones))
	for k, v := range s.zones {
		snap[k] = v
	}
	return snap
}

// PutRecord adds or replaces a single record within the named zone using
// copy-on-write: it publishes a brand-new Zone value so concurrent readers
// (AXFR, CNAME chasing) always see a consistent snapshot.
//
// When rec.ID > 0 and a record with that ID already exists in the rrset it is
// replaced; otherwise the record is appended.
// Creates an empty zone for apex if it does not yet exist.
func (s *RWMutexStore) PutRecord(apex string, rec *iface.Record) error {
	apex = normalizeName(apex)
	recName := normalizeName(rec.Name)
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.zones[apex]
	var oldRecs map[iface.RecordKey][]*iface.Record
	if old != nil {
		oldRecs = old.Records
	}

	// shallow-copy the records map so we don't mutate the old Zone
	newRecs := make(map[iface.RecordKey][]*iface.Record, len(oldRecs)+1)
	for k, v := range oldRecs {
		newRecs[k] = v
	}

	key := iface.RecordKey{Name: recName, Qtype: rec.Type}
	existing := newRecs[key]

	var newSlice []*iface.Record
	if rec.ID > 0 {
		for i, r := range existing {
			if r.ID == rec.ID {
				newSlice = make([]*iface.Record, len(existing))
				copy(newSlice, existing)
				newSlice[i] = rec
				break
			}
		}
	}
	if newSlice == nil {
		newSlice = make([]*iface.Record, len(existing)+1)
		copy(newSlice, existing)
		newSlice[len(existing)] = rec
	}
	newRecs[key] = newSlice

	newZone := &iface.Zone{Name: apex, Records: newRecs}
	if old != nil {
		newZone.SOA = old.SOA
		newZone.NS = old.NS
	}
	s.zones[apex] = newZone
	return nil
}

// SetSOA updates the SOA record for apex's zone using copy-on-write, without
// touching its Records — mirrors PutRecord's pattern. No-op (zone stays
// absent) when apex has no zone yet; the SOA is picked up automatically the
// next time the zone is created via Update/PutRecord, since agent.go always
// has the latest cached SOA on hand when it does that.
func (s *RWMutexStore) SetSOA(apex string, soa *dns.SOA) error {
	apex = normalizeName(apex)
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.zones[apex]
	if old == nil {
		return nil
	}

	newZone := &iface.Zone{Name: apex, Records: old.Records, SOA: soa, NS: old.NS}
	s.zones[apex] = newZone
	return nil
}

// SetNS updates the NS records for apex's zone using copy-on-write, without
// touching its Records/SOA — mirrors SetSOA. No-op (zone stays absent) when
// apex has no zone yet; picked up automatically the next time the zone is
// created, since agent.go always has the latest cached hosts config on hand.
func (s *RWMutexStore) SetNS(apex string, ns []*dns.NS) error {
	apex = normalizeName(apex)
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.zones[apex]
	if old == nil {
		return nil
	}

	newZone := &iface.Zone{Name: apex, Records: old.Records, SOA: old.SOA, NS: ns}
	s.zones[apex] = newZone
	return nil
}

// ZoneCount returns the number of zones in the store. O(1).
func (s *RWMutexStore) ZoneCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.zones)
}

// DropRecord removes the record with the given ID from apex's zone using
// copy-on-write. No-op when the zone or record is absent from the store.
func (s *RWMutexStore) DropRecord(apex string, id int64) error {
	apex = normalizeName(apex)
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.zones[apex]
	if old == nil {
		return nil
	}

	var foundKey iface.RecordKey
	foundIdx := -1
	for k, recs := range old.Records {
		for i, r := range recs {
			if r.ID == id {
				foundKey = k
				foundIdx = i
				break
			}
		}
		if foundIdx >= 0 {
			break
		}
	}
	if foundIdx < 0 {
		return nil
	}

	newRecs := make(map[iface.RecordKey][]*iface.Record, len(old.Records))
	for k, v := range old.Records {
		newRecs[k] = v
	}
	existing := old.Records[foundKey]
	if len(existing) == 1 {
		delete(newRecs, foundKey)
	} else {
		newSlice := make([]*iface.Record, 0, len(existing)-1)
		newSlice = append(newSlice, existing[:foundIdx]...)
		newSlice = append(newSlice, existing[foundIdx+1:]...)
		newRecs[foundKey] = newSlice
	}

	s.zones[apex] = &iface.Zone{Name: apex, Records: newRecs, SOA: old.SOA}
	return nil
}
