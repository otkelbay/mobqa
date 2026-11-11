package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolation is the SQLSTATE Postgres reports when an insert or update breaks a unique constraint.
const uniqueViolation = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
