package identity

import (
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
	"github.com/satym-in/tenant-saas-backend/pkg/dberr"
)

// classifyDBError delegates to pkg/dberr, which holds the single definition of
// SQLSTATE-to-error-code mapping shared by every module. Kept as a thin local
// alias so existing call sites in this package read unchanged.
func classifyDBError(err error) apperror.Code {
	return dberr.Classify(err)
}
