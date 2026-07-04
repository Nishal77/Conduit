package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("record not found")

// ErrConflict is returned when a unique constraint would be violated.
var ErrConflict = errors.New("record already exists")

// ErrDeleted is returned when operating on a soft-deleted record.
var ErrDeleted = errors.New("record has been deleted")

// pgUniqueViolation is the PostgreSQL error code for a unique constraint
// violation (23505).
const pgUniqueViolation = "23505"

// isUniqueViolation reports whether err is a PostgreSQL unique constraint
// violation, so store methods can translate it into the ErrConflict
// sentinel without callers ever needing to import pgconn themselves.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
