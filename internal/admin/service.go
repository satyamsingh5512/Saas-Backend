// Package admin implements the organization analytics overview backing the
// admin dashboard: one tenant-scoped snapshot of users, teams, projects,
// invitations, API keys, storage, audit volume, and engagement.
//
// Security posture, stated plainly:
//   - Every count runs inside txscope.WithTenantTx for the CALLER's tenant,
//     so PostgreSQL RLS enforces the scope even if a query below forgot its
//     own WHERE clause. There is no cross-tenant query path in this package.
//   - The route is gated by org:view (org leadership), not by a new
//     super-admin concept the RBAC model does not have.
//   - Platform-wide totals (all tenants) are intentionally NOT served here:
//     app_user cannot see across tenants by design (fail-closed RLS), and
//     inventing a bypass for a dashboard widget would trade the platform's
//     core guarantee for convenience. Fleet totals live in /metrics via the
//     aggregate-only platform_stats() function (migrations/000016), which
//     exposes counts and no tenant PII.
package admin

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/pkg/txscope"
	"gorm.io/gorm"
)

// Overview is the dashboard snapshot for one organization.
type Overview struct {
	Organization Organization `json:"organization"`
	Users        Users        `json:"users"`
	Teams        int64        `json:"teams"`
	Projects     int64        `json:"projects"`
	Invitations  Invitations  `json:"invitations"`
	APIKeys      APIKeys      `json:"api_keys"`
	Storage      Storage      `json:"storage"`
	Audit        Audit        `json:"audit"`
	GeneratedAt  time.Time    `json:"generated_at"`
}

// Organization identifies the scoped tenant and its plan.
type Organization struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	PlanCode  string    `json:"plan_code"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Users splits the member roster into headline numbers the dashboard needs:
// total provisioned seats, how many can actually sign in, and 7-day engagement.
type Users struct {
	Total         int64 `json:"total"`
	Active        int64 `json:"active"`
	Disabled      int64 `json:"disabled"`
	ActiveUsers7d int64 `json:"active_users_7d"`
}

// Invitations distinguishes actionable invites from terminal ones.
type Invitations struct {
	Pending  int64 `json:"pending"`
	Accepted int64 `json:"accepted_30d"`
}

// APIKeys reports credential hygiene at a glance.
type APIKeys struct {
	Active  int64 `json:"active"`
	Revoked int64 `json:"revoked"`
	Expired int64 `json:"expired"`
}

// Storage reports bytes recorded in the file catalog for this tenant.
type Storage struct {
	Files     int64 `json:"files"`
	UsedBytes int64 `json:"used_bytes"`
}

// Audit reports compliance-stream volume.
type Audit struct {
	Events30d int64 `json:"events_30d"`
}

// Service queries the overview snapshot.
type Service struct {
	db *gorm.DB
}

// NewService builds the admin service over the shared pool.
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// GetOverview returns the dashboard snapshot for tenantID. All RLS-protected
// counts run inside one tenant-scoped transaction; the tenants-table lookup
// is global-catalog (no RLS) and filtered by primary key.
func (s *Service) GetOverview(ctx context.Context, tenantID uuid.UUID) (*Overview, error) {
	out := &Overview{GeneratedAt: time.Now().UTC()}

	// Organization identity: tenants is a global catalog table (no RLS),
	// so this is a plain primary-key lookup, never a tenant-scoped scan.
	type orgRow struct {
		ID        uuid.UUID
		Name      string
		Slug      string
		PlanCode  string `gorm:"column:plan_code"`
		Status    string
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	var org orgRow
	if err := s.db.WithContext(ctx).Table("tenants").
		Where("id = ?", tenantID).Take(&org).Error; err != nil {
		return nil, err
	}
	out.Organization = Organization{
		ID: org.ID, Name: org.Name, Slug: org.Slug,
		PlanCode: org.PlanCode, Status: org.Status, CreatedAt: org.CreatedAt,
	}

	// Everything below is tenant-scoped: RLS confines the transaction to
	// this tenant, and each query repeats the tenant predicate explicitly
	// so the scope is visible at the call site too.
	err := txscope.WithTenantTxID(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		count := func(table, where string, args ...any) (int64, error) {
			var n int64
			q := tx.Table(table).Where("tenant_id = ?", tenantID)
			if where != "" {
				q = q.Where(where, args...)
			}
			if err := q.Count(&n).Error; err != nil {
				return 0, err
			}
			return n, nil
		}
		var err error
		if out.Users.Total, err = count("users", "deleted_at IS NULL"); err != nil {
			return err
		}
		if out.Users.Active, err = count("users", "deleted_at IS NULL AND status = 'active'"); err != nil {
			return err
		}
		if out.Users.Disabled, err = count("users", "deleted_at IS NULL AND status = 'disabled'"); err != nil {
			return err
		}
		if out.Users.ActiveUsers7d, err = count("users", "deleted_at IS NULL AND last_login_at > NOW() - INTERVAL '7 days'"); err != nil {
			return err
		}
		if out.Teams, err = count("teams", "deleted_at IS NULL"); err != nil {
			return err
		}
		if out.Projects, err = count("projects", "deleted_at IS NULL"); err != nil {
			return err
		}
		if out.Invitations.Pending, err = count("invitations", "status = 'pending' AND expires_at > NOW()"); err != nil {
			return err
		}
		if out.Invitations.Accepted, err = count("invitations", "status = 'accepted' AND accepted_at > NOW() - INTERVAL '30 days'"); err != nil {
			return err
		}
		if out.APIKeys.Active, err = count("api_keys", "revoked_at IS NULL AND (expires_at IS NULL OR expires_at > NOW())"); err != nil {
			return err
		}
		if out.APIKeys.Revoked, err = count("api_keys", "revoked_at IS NOT NULL"); err != nil {
			return err
		}
		if out.APIKeys.Expired, err = count("api_keys", "revoked_at IS NULL AND expires_at IS NOT NULL AND expires_at <= NOW()"); err != nil {
			return err
		}
		if out.Storage.Files, err = count("files", ""); err != nil {
			return err
		}
		var usedBytes *int64
		if err := tx.Table("files").Where("tenant_id = ?", tenantID).
			Select("COALESCE(SUM(size_bytes),0)").Scan(&usedBytes).Error; err != nil {
			return err
		}
		if usedBytes != nil {
			out.Storage.UsedBytes = *usedBytes
		}
		if out.Audit.Events30d, err = count("audit_logs", "created_at > NOW() - INTERVAL '30 days'"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
