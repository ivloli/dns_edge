package pg

import (
	"context"
	"fmt"
	"strings"

	mdns "github.com/miekg/dns"
	"go.uber.org/zap"

	"dns-edge/internal/iface"
)

const queryLoadAll = `
SELECT
    z.name  AS zone_name,
    r.id    AS record_id,
    r.name  AS rec_name,
    r.type  AS rec_type,
    r.ttl,
    r.value,
    r.weight
FROM zones z
JOIN records r ON r.zone_id = z.id
WHERE z.deleted_at IS NULL
  AND r.deleted_at IS NULL
ORDER BY z.name, r.name, r.type
`

// LoadAll reads every active zone and record from PostgreSQL and populates
// zoneStore. Called once at startup before the DNS server accepts queries.
//
// Records with unrecognised types or malformed rdata are logged and skipped
// rather than aborting — a single bad row should not block startup.
func (s *Store) LoadAll(ctx context.Context, zoneStore iface.ZoneStore) error {
	rows, err := s.pool.Query(ctx, queryLoadAll)
	if err != nil {
		return fmt.Errorf("pg load: query: %w", err)
	}
	defer rows.Close()

	// collect into a local map first; call zoneStore.Update once per zone
	zones := make(map[string]*iface.Zone)
	loaded, skipped := 0, 0

	for rows.Next() {
		var (
			zoneName, recName, recType, value string
			id                                int64
			ttl, weight                       int
		)
		if err := rows.Scan(&zoneName, &id, &recName, &recType, &ttl, &value, &weight); err != nil {
			return fmt.Errorf("pg load: scan: %w", err)
		}

		// normalise to FQDN — ops may insert records without the trailing dot
		zoneName = fqdn(zoneName)
		recName = fqdn(recName)

		qtype, ok := mdns.StringToType[recType]
		if !ok {
			s.log.Warn("unknown record type, skipping",
				zap.String("type", recType), zap.String("name", recName))
			skipped++
			continue
		}

		// build a canonical RR string and let miekg parse it
		rrStr := fmt.Sprintf("%s %d IN %s %s", recName, ttl, recType, value)
		rr, err := mdns.NewRR(rrStr)
		if err != nil {
			s.log.Warn("invalid RR, skipping", zap.String("rr", rrStr), zap.Error(err))
			skipped++
			continue
		}

		zone, ok := zones[zoneName]
		if !ok {
			zone = &iface.Zone{
				Name:    zoneName,
				Records: make(map[iface.RecordKey][]*iface.Record),
			}
			zones[zoneName] = zone
		}

		key := iface.RecordKey{Name: recName, Qtype: qtype}
		zone.Records[key] = append(zone.Records[key], &iface.Record{
			ID:     id,
			Name:   recName,
			Type:   qtype,
			TTL:    uint32(ttl),
			Value:  value,
			Weight: weight,
			RR:     rr,
		})
		loaded++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg load: rows: %w", err)
	}

	for _, zone := range zones {
		if err := zoneStore.Update(zone); err != nil {
			return fmt.Errorf("pg load: store.Update %q: %w", zone.Name, err)
		}
	}

	s.log.Info("pg load complete",
		zap.Int("zones", len(zones)),
		zap.Int("records", loaded),
		zap.Int("skipped", skipped),
	)
	return nil
}

// fqdn ensures name has a trailing dot.
func fqdn(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}
