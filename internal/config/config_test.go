package config

import (
	"strings"
	"testing"
)

func TestValidateProductionAcceptsRenderDatabaseURL(t *testing.T) {
	cfg := &Config{
		Environment:    "production",
		JWTSecret:      strings.Repeat("s", minimumJWTSecretLength),
		DatabaseURL:    "postgresql://tenant:password@dpg-example:5432/tenant_saas",
		StorageDriver:  "s3",
		S3Region:       "us-east-1",
		S3Bucket:       "tenant-files",
		S3AccessKey:    "access",
		S3SecretKey:    "secret",
		MaxUploadBytes: 25 * 1024 * 1024,
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() returned an unexpected error: %v", err)
	}
}

func TestValidateProductionRejectsMissingSecretsAndLocalDatabase(t *testing.T) {
	cfg := &Config{Environment: "production", DBHost: "localhost"}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("Validate() error = %v, want JWT_SECRET configuration error", err)
	}

	cfg.JWTSecret = strings.Repeat("s", minimumJWTSecretLength)
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Validate() error = %v, want DATABASE_URL configuration error", err)
	}
}

func TestValidateAllowsCompleteSplitProductionDatabaseConfig(t *testing.T) {
	cfg := &Config{
		Environment:    "production",
		JWTSecret:      strings.Repeat("s", minimumJWTSecretLength),
		DBHost:         "postgres.internal",
		DBUser:         "tenant",
		DBPassword:     "password",
		DBName:         "tenant_saas",
		DBMaxOpenConns: 25,
		DBMaxIdleConns: 5,
		RedisPoolSize:  10,
		StorageDriver:  "local",
		StorageRoot:    "/var/lib/tenant-saas/uploads",
		MaxUploadBytes: 25 * 1024 * 1024,
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() returned an unexpected error: %v", err)
	}
}

func TestValidateStorageRequiresS3Credentials(t *testing.T) {
	cfg := &Config{StorageDriver: "s3", S3Region: "us-east-1"}
	if err := cfg.ValidateStorage(); err == nil {
		t.Fatal("ValidateStorage() accepted incomplete S3 configuration")
	}
}

func TestValidateStorageAllowsDevelopmentLocalDefaults(t *testing.T) {
	cfg := &Config{Environment: "development", StorageDriver: "local", StorageRoot: "./data/uploads", MaxUploadBytes: 1024}
	if err := cfg.ValidateStorage(); err != nil {
		t.Fatalf("ValidateStorage() returned an unexpected error: %v", err)
	}
}

func TestLoadReadsTrimmedRedisURL(t *testing.T) {
	t.Setenv("REDIS_URL", "  rediss://:password@redis.example.com:6379/2  ")

	cfg := Load()
	if got, want := cfg.RedisURL, "rediss://:password@redis.example.com:6379/2"; got != want {
		t.Errorf("RedisURL = %q, want %q", got, want)
	}
}
