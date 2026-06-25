package pg

import (
	"context"
	"fmt"
	"time"

	mdns "github.com/miekg/dns"
	"go.uber.org/zap"

	"dns-edge/internal/iface"
)

const queryIncrementalZones = `
SELECT name, deleted_at IS NOT NULL AS is_deleted
FROM zones
WHERE updated_at > $1`

const queryIncrementalRecords = `
SELECT z.name   AS apex,
       r.id,
       r.name,
       r.type,
       r.ttl,
       r.value,
       r.weight,
       r.route_tags,
       r.deleted_at IS NOT NULL AS is_deleted
FROM records r
JOIN zones z ON r.zone_id = z.id
WHERE r.updated_at > $1
  AND z.deleted_at IS NULL`

// IncrementalLoad applies all zone and record changes that occurred after
// since to zoneStore. It is safe to call concurrently with DNS reads.
//
// Deletion handling:
//   - Zone soft-deleted → ZoneStore.Delete(apex)
//   - Record soft-deleted → ZoneStore.DropRecord(apex, id)
//   - Record upserted → ZoneStore.PutRecord(apex, rec)
func (s *Store) IncrementalLoad(ctx context.Context, since time.Time, zoneStore iface.ZoneStore) error {
	// ── 1. Zone-level changes ────────────────────────────────────────────────
	zrows, err := s.pool.Query(ctx, queryIncrementalZones, since)
	if err != nil {
		return fmt.Errorf("pg: incremental zones: %w", err)
	}
	defer zrows.Close()

	for zrows.Next() {
		var name string
		var isDeleted bool
		if err := zrows.Scan(&name, &isDeleted); err != nil {
			return fmt.Errorf("pg: incremental zones: scan: %w", err)
		}
		if isDeleted {
			_ = zoneStore.Delete(fqdn(name))
		}
		// created/restored zones gain records via the record query below
	}
	if err := zrows.Err(); err != nil {
		return fmt.Errorf("pg: incremental zones: rows: %w", err)
	}

	// ── 2. Record-level changes ──────────────────────────────────────────────
	rrows, err := s.pool.Query(ctx, queryIncrementalRecords, since)
	if err != nil {
		return fmt.Errorf("pg: incremental records: %w", err)
	}
	defer rrows.Close()

	updated, deleted, skipped := 0, 0, 0
	for rrows.Next() {
		var (
			apex, recName, typStr, value, routeTags string
			id                                       int64
			ttl, weight                              int
			isDeleted                                bool
		)
		if err := rrows.Scan(&apex, &id, &recName, &typStr, &ttl, &value, &weight, &routeTags, &isDeleted); err != nil {
			return fmt.Errorf("pg: incremental records: scan: %w", err)
		}

		apex = fqdn(apex)
		recName = fqdn(recName)

		if isDeleted {
			_ = zoneStore.DropRecord(apex, id)
			deleted++
			continue
		}

		qtype, ok := mdns.StringToType[typStr]
		if !ok {
			s.log.Warn("incremental: unknown type, skipping",
				zap.String("name", recName), zap.String("type", typStr))
			skipped++
			continue
		}

		rr, err := parseRR(recName, uint32(ttl), typStr, value)
		if err != nil {
			s.log.Warn("incremental: invalid rr, skipping",
				zap.String("name", recName), zap.Error(err))
			skipped++
			continue
		}

		rec := &iface.Record{
			ID: id, Name: recName, Type: qtype,
			TTL: uint32(ttl), Value: value, Weight: weight, RouteTags: routeTags, RR: rr,
		}
		if err := zoneStore.PutRecord(apex, rec); err != nil {
			s.log.Error("incremental: PutRecord failed", zap.Error(err))
		}
		updated++
	}
	if err := rrows.Err(); err != nil {
		return fmt.Errorf("pg: incremental records: rows: %w", err)
	}

	if updated+deleted+skipped > 0 {
		s.log.Info("pg incremental sync",
			zap.Int("updated", updated),
			zap.Int("deleted", deleted),
			zap.Int("skipped", skipped),
		)
	}
	return nil
}
