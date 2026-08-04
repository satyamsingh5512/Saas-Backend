package apikeys

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// Recorder is the slice of internal/audit this module needs. Key creation and
// revocation are credential events and are always audited.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// Service implements API key lifecycle and authentication.
type Service struct {
	repo   *Repository
	audit  Recorder
	logger *slog.Logger
	// grantable is the set of permission codes a key may be scoped to. Scopes are
	// validated against it so a key cannot be minted with a capability the
	// platform does not define.
	grantable map[string]struct{}
}

func NewService(repo *Repository, recorder Recorder, logger *slog.Logger, grantableScopes []string) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	grantable := make(map[string]struct{}, len(grantableScopes))
	for _, s := range grantableScopes {
		grantable[s] = struct{}{}
	}
	return &Service{repo: repo, audit: recorder, logger: logger, grantable: grantable}
}

// CreateInput is the validated input for minting a key.
type CreateInput struct {
	Name   string
	Scopes []string
	// ExpiresAt is optional. A key with no expiry is valid until revoked, which is
	// convenient but means a leak has unlimited lifetime, so callers are encouraged
	// to set one.
	ExpiresAt *time.Time
	// OwnerUserID attributes the key to the human who created it, so a departing
	// employee's keys are discoverable.
	OwnerUserID uuid.UUID
}

// Create mints a new API key and returns its plaintext secret exactly once.
func (s *Service) Create(ctx context.Context, entry audit.Entry, tenantID uuid.UUID, in CreateInput) (*CreateResult, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, apperror.New(apperror.CodeValidation, "name is required")
	}

	scopes, err := s.validateScopes(in.Scopes)
	if err != nil {
		return nil, err
	}

	if in.ExpiresAt != nil && in.ExpiresAt.Before(time.Now()) {
		return nil, apperror.New(apperror.CodeValidation, "expires_at must be in the future")
	}

	plaintext, prefix, hash, err := GenerateKey()
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to generate api key", err)
	}

	owner := in.OwnerUserID
	key := &Key{
		ID:        uuid.New(),
		TenantID:  tenantID,
		UserID:    &owner,
		Name:      name,
		KeyPrefix: prefix,
		KeyHash:   hash,
		Scopes:    pq.StringArray(scopes),
		ExpiresAt: in.ExpiresAt,
		CreatedAt: time.Now(),
	}

	if err := s.repo.Create(ctx, key); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to create api key", err)
	}

	if s.audit != nil {
		keyID := key.ID
		entry.Action = audit.ActionAPIKeyCreated
		entry.TargetType = "api_key"
		entry.TargetID = &keyID
		// The secret is never recorded, only its non-sensitive display prefix.
		entry.Metadata = map[string]any{"name": name, "key_prefix": prefix, "scopes": scopes}
		s.audit.Record(ctx, entry)
	}

	return &CreateResult{Key: key, Secret: plaintext}, nil
}

// List returns the tenant's API keys.
func (s *Service) List(ctx context.Context, page, pageSize int) ([]Key, int64, error) {
	keys, total, err := s.repo.List(ctx, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list api keys", err)
	}
	return keys, total, nil
}

// Revoke permanently disables a key.
func (s *Service) Revoke(ctx context.Context, entry audit.Entry, keyID uuid.UUID) error {
	key, err := s.repo.FindByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return apperror.New(apperror.CodeNotFound, "api key not found")
		}
		return apperror.Wrap(apperror.CodeInternal, "failed to load api key", err)
	}

	if err := s.repo.Revoke(ctx, keyID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return apperror.New(apperror.CodeConflict, "api key is already revoked")
		}
		return apperror.Wrap(apperror.CodeInternal, "failed to revoke api key", err)
	}

	if s.audit != nil {
		id := keyID
		entry.Action = audit.ActionAPIKeyRevoked
		entry.TargetType = "api_key"
		entry.TargetID = &id
		entry.Metadata = map[string]any{"name": key.Name, "key_prefix": key.KeyPrefix}
		s.audit.Record(ctx, entry)
	}
	return nil
}

// Authenticated is the result of successfully authenticating an API key.
type Authenticated struct {
	KeyID    uuid.UUID
	TenantID uuid.UUID
	UserID   *uuid.UUID
	Scopes   []string
}

// Authenticate validates a presented plaintext key.
//
// Every rejection returns the same opaque error. Telling a caller whether a key
// was unknown, expired, or revoked would let them mine information about valid
// credentials, and none of those distinctions help a legitimate client.
func (s *Service) Authenticate(ctx context.Context, plaintext string) (*Authenticated, error) {
	if !LooksLikeAPIKey(plaintext) {
		return nil, apperror.New(apperror.CodeUnauthorized, "invalid api key")
	}

	key, err := s.repo.FindByHash(ctx, HashKey(plaintext))
	if err != nil {
		return nil, apperror.New(apperror.CodeUnauthorized, "invalid api key")
	}
	if !key.Active() {
		return nil, apperror.New(apperror.CodeUnauthorized, "invalid api key")
	}
	if err := s.repo.ValidateCredentialState(ctx, key.TenantID, key.UserID); err != nil {
		return nil, apperror.New(apperror.CodeUnauthorized, "invalid api key")
	}

	// Best-effort usage telemetry; a failure here must not reject a valid request.
	if err := s.repo.TouchLastUsed(ctx, key.TenantID, key.ID); err != nil {
		s.logger.WarnContext(ctx, "failed to update api key last_used_at",
			slog.Any("error", err), slog.String("key_id", key.ID.String()))
	}

	return &Authenticated{
		KeyID:    key.ID,
		TenantID: key.TenantID,
		UserID:   key.UserID,
		Scopes:   []string(key.Scopes),
	}, nil
}

// validateScopes rejects unknown scope codes and de-duplicates the rest.
func (s *Service) validateScopes(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, apperror.New(apperror.CodeValidation,
			"at least one scope is required; a key with no scopes cannot do anything")
	}

	seen := make(map[string]struct{}, len(requested))
	out := make([]string, 0, len(requested))
	for _, raw := range requested {
		scope := strings.TrimSpace(raw)
		if scope == "" {
			continue
		}
		if len(s.grantable) > 0 {
			if _, ok := s.grantable[scope]; !ok {
				return nil, apperror.New(apperror.CodeValidation, "unknown scope: "+scope)
			}
		}
		if _, dup := seen[scope]; dup {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}

	if len(out) == 0 {
		return nil, apperror.New(apperror.CodeValidation, "at least one valid scope is required")
	}
	return out, nil
}
