package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

const maxFilenameRunes = 255

type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

type QuotaChecker interface {
	CheckStorageQuota(ctx context.Context, tenantID uuid.UUID, additionalBytes int64) error
}

type Service struct {
	repo     *Repository
	store    ObjectStore
	audit    Recorder
	quotas   QuotaChecker
	maxBytes int64
}

func NewService(repo *Repository, store ObjectStore, recorder Recorder, quotas QuotaChecker, maxBytes int64) *Service {
	return &Service{repo: repo, store: store, audit: recorder, quotas: quotas, maxBytes: maxBytes}
}

type UploadInput struct {
	ProjectID   *uuid.UUID
	Name        string
	ContentType string
	SizeBytes   int64
	Content     io.Reader
}

func (s *Service) Upload(ctx context.Context, entry audit.Entry, tenantID, userID uuid.UUID, in UploadInput) (*File, error) {
	name, err := cleanFilename(in.Name)
	if err != nil {
		return nil, err
	}
	if in.SizeBytes <= 0 {
		return nil, apperror.New(apperror.CodeValidation, "file must not be empty")
	}
	if s.maxBytes > 0 && in.SizeBytes > s.maxBytes {
		return nil, apperror.New(apperror.CodeUnprocessable, fmt.Sprintf("file exceeds the upload limit of %d bytes", s.maxBytes))
	}
	if in.Content == nil {
		return nil, apperror.New(apperror.CodeValidation, "file content is required")
	}
	if in.ProjectID != nil {
		exists, err := s.repo.ProjectExists(ctx, *in.ProjectID)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal, "failed to verify project", err)
		}
		if !exists {
			return nil, apperror.New(apperror.CodeNotFound, "project not found in this organization")
		}
	}
	if s.quotas != nil {
		if err := s.quotas.CheckStorageQuota(ctx, tenantID, in.SizeBytes); err != nil {
			return nil, err
		}
	}

	fileID := uuid.New()
	key := tenantID.String() + "/" + fileID.String()
	contentType := strings.TrimSpace(in.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	sha256, err := s.store.Put(ctx, key, in.Content, in.SizeBytes, contentType)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to store file", err)
	}

	file := &File{
		ID:           fileID,
		TenantID:     tenantID,
		ProjectID:    in.ProjectID,
		UploadedBy:   userID,
		OriginalName: name,
		StorageKey:   key,
		ContentType:  contentType,
		SizeBytes:    in.SizeBytes,
		SHA256:       sha256,
	}
	if err := s.repo.Create(ctx, file); err != nil {
		_ = s.store.Delete(ctx, key)
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to record file", err)
	}

	if s.audit != nil {
		entry.Action = audit.ActionFileUploaded
		entry.TargetType = audit.TargetFile
		entry.TargetID = &file.ID
		entry.Metadata = map[string]any{
			"name":         file.OriginalName,
			"size_bytes":   file.SizeBytes,
			"content_type": file.ContentType,
		}
		s.audit.Record(ctx, entry)
	}
	return file, nil
}

func (s *Service) List(ctx context.Context, projectID *uuid.UUID, page, pageSize int) ([]File, int64, error) {
	items, total, err := s.repo.List(ctx, projectID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternal, "failed to list files", err)
	}
	return items, total, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*File, error) {
	file, err := s.repo.FindByID(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, apperror.New(apperror.CodeNotFound, "file not found")
	}
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, "failed to load file", err)
	}
	return file, nil
}

func (s *Service) Open(ctx context.Context, id uuid.UUID) (*File, io.ReadCloser, error) {
	file, err := s.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	reader, err := s.store.Open(ctx, file.StorageKey)
	if errors.Is(err, ErrObjectNotFound) {
		return nil, nil, apperror.New(apperror.CodeNotFound, "file content not found")
	}
	if err != nil {
		return nil, nil, apperror.Wrap(apperror.CodeInternal, "failed to open file", err)
	}
	return file, reader, nil
}

func (s *Service) Delete(ctx context.Context, entry audit.Entry, id uuid.UUID) error {
	file, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, file.StorageKey); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to delete file content", err)
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "failed to delete file record", err)
	}
	if s.audit != nil {
		entry.Action = audit.ActionFileDeleted
		entry.TargetType = audit.TargetFile
		entry.TargetID = &id
		entry.Metadata = map[string]any{"name": file.OriginalName, "size_bytes": file.SizeBytes}
		s.audit.Record(ctx, entry)
	}
	return nil
}

func cleanFilename(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\x00", ""))
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return "", apperror.New(apperror.CodeValidation, "filename is required")
	}
	if utf8.RuneCountInString(name) > maxFilenameRunes {
		return "", apperror.New(apperror.CodeValidation, "filename is too long")
	}
	return name, nil
}
