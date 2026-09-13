// Package files implements tenant-scoped file metadata and storage-backed
// uploads. File bytes live in an object store; PostgreSQL stores ownership,
// quota accounting, and the information needed to retrieve them safely.
package files

import (
	"time"

	"github.com/google/uuid"
)

// File mirrors the `files` table. StorageKey is opaque to clients and never
// contains a user-controlled filename.
type File struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID     uuid.UUID  `gorm:"column:tenant_id" json:"tenant_id"`
	ProjectID    *uuid.UUID `gorm:"column:project_id" json:"project_id,omitempty"`
	UploadedBy   uuid.UUID  `gorm:"column:uploaded_by" json:"uploaded_by"`
	OriginalName string     `gorm:"column:original_name" json:"original_name"`
	StorageKey   string     `gorm:"column:storage_key" json:"-"`
	ContentType  string     `gorm:"column:content_type" json:"content_type"`
	SizeBytes    int64      `gorm:"column:size_bytes" json:"size_bytes"`
	SHA256       string     `gorm:"column:sha256" json:"sha256"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (File) TableName() string { return "files" }
