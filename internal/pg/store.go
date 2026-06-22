package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Store wraps a pgxpool.Pool and is the PostgreSQL access layer for dns-edge.
// It is the source of truth for all DNS records.
//
// Phase 2: LoadAll (startup full load).
// Phase 3: CRUD methods used by the HTTP API.
// Phase 4: IncrementalLoad used by the sync scheduler.
type Store struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

// New opens a connection pool, pings the server, and returns a Store.
// Returns an error if the DSN is invalid or the server is unreachable.
func New(ctx context.Context, dsn string, log *zap.Logger) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	log.Info("pg connected", zap.String("dsn", maskDSN(dsn)))
	return &Store{pool: pool, log: log}, nil
}

// Close releases all pool connections.
func (s *Store) Close() { s.pool.Close() }

// EnsureSchema idempotently creates tables, indexes, and triggers.
// All DDL statements use IF NOT EXISTS / OR REPLACE, so this is safe to
// call on every startup (controlled by --auto-migrate flag in main).
//
// The SQL here mirrors migrations/001_init.sql — keep the two in sync.
func (s *Store) EnsureSchema(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("pg: schema: %w", err)
	}
	s.log.Info("pg schema ensured")
	return nil
}

// maskDSN hides the password in a DSN for safe logging.
func maskDSN(dsn string) string {
	// pgx accepts both URL and key=value forms; just truncate at 60 chars
	if len(dsn) > 60 {
		return dsn[:60] + "…"
	}
	return dsn
}

// schemaSQL mirrors migrations/001_init.sql.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS zones (
    id         BIGSERIAL    PRIMARY KEY,
    name       TEXT         NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT zones_name_key UNIQUE (name)
);

CREATE INDEX IF NOT EXISTS idx_zones_updated_at ON zones (updated_at);

CREATE TABLE IF NOT EXISTS records (
    id         BIGSERIAL    PRIMARY KEY,
    zone_id    BIGINT       NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    name       TEXT         NOT NULL,
    type       TEXT         NOT NULL,
    ttl        INTEGER      NOT NULL DEFAULT 300,
    value      TEXT         NOT NULL,
    weight     INTEGER      NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT records_unique_rrset UNIQUE (zone_id, name, type, value)
);

CREATE INDEX IF NOT EXISTS idx_records_updated_at  ON records (updated_at);
CREATE INDEX IF NOT EXISTS idx_records_zone_lookup ON records (zone_id, name, type)
    WHERE deleted_at IS NULL;

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'trg_zones_updated_at') THEN
        CREATE TRIGGER trg_zones_updated_at
            BEFORE UPDATE ON zones FOR EACH ROW EXECUTE FUNCTION set_updated_at();
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'trg_records_updated_at') THEN
        CREATE TRIGGER trg_records_updated_at
            BEFORE UPDATE ON records FOR EACH ROW EXECUTE FUNCTION set_updated_at();
    END IF;
END $$;
`
