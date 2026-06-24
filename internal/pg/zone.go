package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"dns-edge/internal/iface"
)

// CreateZone inserts a new active zone, or returns the existing one if a zone
// with the same name already exists. The second return value is true when the
// zone was newly created, false when it already existed.
func (s *Store) CreateZone(ctx context.Context, name string) (iface.ZoneMeta, bool, error) {
	var id int64
	var created bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO zones(name) VALUES($1)
		ON CONFLICT (name) DO NOTHING
		RETURNING id`, name).Scan(&id)
	if err == nil {
		return iface.ZoneMeta{ID: id, Name: name}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return iface.ZoneMeta{}, false, fmt.Errorf("pg: create zone %q: %w", name, err)
	}
	// Already exists — fetch the existing row.
	err = s.pool.QueryRow(ctx,
		`SELECT id FROM zones WHERE name=$1 AND deleted_at IS NULL`, name).Scan(&id)
	if err != nil {
		return iface.ZoneMeta{}, false, fmt.Errorf("pg: create zone %q: fetch existing: %w", name, err)
	}
	_ = created
	return iface.ZoneMeta{ID: id, Name: name}, false, nil
}

// GetZone returns metadata for an active zone. Returns ErrNotFound when the
// zone does not exist or has been soft-deleted.
func (s *Store) GetZone(ctx context.Context, apex string) (iface.ZoneMeta, error) {
	var z iface.ZoneMeta
	err := s.pool.QueryRow(ctx,
		`SELECT id, name FROM zones WHERE name=$1 AND deleted_at IS NULL`, apex).
		Scan(&z.ID, &z.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return iface.ZoneMeta{}, ErrNotFound
	}
	if err != nil {
		return iface.ZoneMeta{}, fmt.Errorf("pg: get zone %q: %w", apex, err)
	}
	return z, nil
}

// SoftDeleteZone marks a zone and cascades to its records via the FK trigger.
// Returns ErrNotFound if the zone is absent or already deleted.
func (s *Store) SoftDeleteZone(ctx context.Context, apex string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE zones SET deleted_at=NOW() WHERE name=$1 AND deleted_at IS NULL`, apex)
	if err != nil {
		return fmt.Errorf("pg: delete zone %q: %w", apex, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListZones returns metadata for every active zone, ordered by name.
func (s *Store) ListZones(ctx context.Context) ([]iface.ZoneMeta, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name FROM zones WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("pg: list zones: %w", err)
	}
	defer rows.Close()

	var zones []iface.ZoneMeta
	for rows.Next() {
		var z iface.ZoneMeta
		if err := rows.Scan(&z.ID, &z.Name); err != nil {
			return nil, fmt.Errorf("pg: list zones: scan: %w", err)
		}
		zones = append(zones, z)
	}
	return zones, rows.Err()
}
