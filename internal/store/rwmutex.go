package store

import (
	"fmt"
	"strings"
	"sync"

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

// Lookup returns the rrset for (name, qtype), walking up the label hierarchy
// to find the authoritative zone. Returns nil if not found.
func (s *RWMutexStore) Lookup(name string, qtype uint16) []*iface.Record {
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lockedFindZone(name)
}

// NameExists reports whether name has any record of any type in the store.
// Used to distinguish NXDOMAIN (name absent) from NODATA (no records of the
// requested type).
func (s *RWMutexStore) NameExists(name string) bool {
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
// name. Must be called with at least a read lock held.
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

// Update atomically replaces (or inserts) the entire zone.
func (s *RWMutexStore) Update(zone *iface.Zone) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.zones[zone.Name] = zone
	return nil
}

// Delete removes the zone with the given apex FQDN.
func (s *RWMutexStore) Delete(apex string) error {
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

	key := iface.RecordKey{Name: rec.Name, Qtype: rec.Type}
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
	}
	s.zones[apex] = newZone
	return nil
}

// DropRecord removes the record with the given ID from apex's zone using
// copy-on-write. No-op when the zone or record is absent from the store.
func (s *RWMutexStore) DropRecord(apex string, id int64) error {
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
