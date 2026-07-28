// Package dberr classifies PostgreSQL driver errors into the application's
// typed error codes.
//
// It exists so that "duplicate key" is translated to a 409 identically in every
// module. The alternative -- each module inspecting SQLSTATE itself -- reliably
// drifts, and the failure mode is silent: a missed unique-violation check turns a
// user-correctable conflict into an opaque 500.
package dberr

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// PostgreSQL SQLSTATE codes this package recognizes.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
	sqlStateNotNullViolation    = "23502"
)

// IsUniqueViolation reports whether err is a unique-constraint violation,
// meaning the caller submitted a value that collides with an existing row.
func IsUniqueViolation(err error) bool {
	return hasSQLState(err, sqlStateUniqueViolation)
}

// IsForeignKeyViolation reports whether err is a foreign-key violation, which
// generally means the caller referenced a row that does not exist.
func IsForeignKeyViolation(err error) bool {
	return hasSQLState(err, sqlStateForeignKeyViolation)
}

// IsCheckViolation reports whether err is a CHECK-constraint violation, e.g. a
// status value outside the schema's permitted set.
func IsCheckViolation(err error) bool {
	return hasSQLState(err, sqlStateCheckViolation)
}

// Classify maps a database error to an apperror.Code.
//
// Only genuinely client-correctable violations map to 4xx codes. A NOT NULL
// violation stays CodeInternal on purpose: it means application code failed to
// populate a required column, which is a bug, and reporting it as a 400 would
// blame the caller and bury the defect.
func Classify(err error) apperror.Code {
	switch {
	case IsUniqueViolation(err):
		return apperror.CodeConflict
	case IsForeignKeyViolation(err), IsCheckViolation(err):
		return apperror.CodeValidation
	default:
		return apperror.CodeInternal
	}
}

func hasSQLState(err error, state string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == state
}
