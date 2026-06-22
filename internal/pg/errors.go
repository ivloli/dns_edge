package pg

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrNotFound is returned when a query matches zero rows.
	ErrNotFound = errors.New("pg: not found")

	// ErrConflict is returned on a unique-constraint violation (SQLSTATE 23505).
	ErrConflict = errors.New("pg: already exists")
)

// isConflict reports whether err is a PostgreSQL unique-violation error.
func isConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
