package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	mdns "github.com/miekg/dns"

	"dns-edge/internal/iface"
)

// Compile-time interface check.
var _ iface.RecordStore = (*Store)(nil)

const queryCreateRecord = `
INSERT INTO records(zone_id, name, type, ttl, value, weight)
VALUES($1, $2, $3, $4, $5, $6)
RETURNING id, name, type, ttl, value, weight`

const queryUpdateRecord = `
UPDATE records
SET name=$3, type=$4, ttl=$5, value=$6, weight=$7, deleted_at=NULL
WHERE id=$1 AND zone_id=$2
RETURNING id, name, type, ttl, value, weight`

const querySoftDeleteRecord = `
UPDATE records SET deleted_at=NOW()
WHERE id=$1 AND zone_id=$2 AND deleted_at IS NULL`

const queryListRecords = `
SELECT r.id, r.name, r.type, r.ttl, r.value, r.weight
FROM records r
JOIN zones z ON r.zone_id = z.id
WHERE z.name=$1 AND z.deleted_at IS NULL AND r.deleted_at IS NULL
ORDER BY r.name, r.type`

// CreateRecord inserts a new record under the given zone.
// Returns ErrConflict when (zone_id, name, type, value) already exists.
// The returned Record has its RR field populated.
func (s *Store) CreateRecord(ctx context.Context, zoneID int64, rec *iface.Record) (*iface.Record, error) {
	typStr := mdns.TypeToString[rec.Type]
	if typStr == "" {
		typStr = fmt.Sprintf("TYPE%d", rec.Type)
	}

	var out iface.Record
	var typName string
	err := s.pool.QueryRow(ctx, queryCreateRecord,
		zoneID, rec.Name, typStr, rec.TTL, rec.Value, rec.Weight).
		Scan(&out.ID, &out.Name, &typName, &out.TTL, &out.Value, &out.Weight)
	if err != nil {
		if isConflict(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("pg: create record: %w", err)
	}
	out.Type = mdns.StringToType[typName]

	rr, err := parseRR(out.Name, out.TTL, typName, out.Value)
	if err != nil {
		return nil, fmt.Errorf("pg: create record: parse rr: %w", err)
	}
	out.RR = rr
	return &out, nil
}

// UpdateRecord replaces fields of an existing record. The record must belong
// to zoneID. Returns ErrNotFound when 0 rows were affected.
func (s *Store) UpdateRecord(ctx context.Context, zoneID, id int64, rec *iface.Record) (*iface.Record, error) {
	typStr := mdns.TypeToString[rec.Type]
	if typStr == "" {
		typStr = fmt.Sprintf("TYPE%d", rec.Type)
	}

	var out iface.Record
	var typName string
	err := s.pool.QueryRow(ctx, queryUpdateRecord,
		id, zoneID, rec.Name, typStr, rec.TTL, rec.Value, rec.Weight).
		Scan(&out.ID, &out.Name, &typName, &out.TTL, &out.Value, &out.Weight)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isConflict(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("pg: update record %d: %w", id, err)
	}
	out.Type = mdns.StringToType[typName]

	rr, err := parseRR(out.Name, out.TTL, typName, out.Value)
	if err != nil {
		return nil, fmt.Errorf("pg: update record: parse rr: %w", err)
	}
	out.RR = rr
	return &out, nil
}

// SoftDeleteRecord marks a record deleted. Returns ErrNotFound when 0 rows affected.
func (s *Store) SoftDeleteRecord(ctx context.Context, zoneID, id int64) error {
	tag, err := s.pool.Exec(ctx, querySoftDeleteRecord, id, zoneID)
	if err != nil {
		return fmt.Errorf("pg: delete record %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListRecords returns all active records for the named zone, ordered by name
// and type. Each record has its RR field populated.
func (s *Store) ListRecords(ctx context.Context, apex string) ([]*iface.Record, error) {
	rows, err := s.pool.Query(ctx, queryListRecords, apex)
	if err != nil {
		return nil, fmt.Errorf("pg: list records: %w", err)
	}
	defer rows.Close()

	var recs []*iface.Record
	for rows.Next() {
		var r iface.Record
		var typName string
		if err := rows.Scan(&r.ID, &r.Name, &typName, &r.TTL, &r.Value, &r.Weight); err != nil {
			return nil, fmt.Errorf("pg: list records: scan: %w", err)
		}
		r.Type = mdns.StringToType[typName]
		rr, err := parseRR(r.Name, r.TTL, typName, r.Value)
		if err != nil {
			s.log.Sugar().Warnf("list records: skip malformed rr %s %s %s: %v", r.Name, typName, r.Value, err)
			continue
		}
		r.RR = rr
		recs = append(recs, &r)
	}
	return recs, rows.Err()
}

// parseRR builds and parses a canonical RR string.
func parseRR(name string, ttl uint32, typStr, value string) (mdns.RR, error) {
	return mdns.NewRR(fmt.Sprintf("%s %d IN %s %s", name, ttl, typStr, value))
}
