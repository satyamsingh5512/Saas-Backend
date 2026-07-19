package db

import (
	"testing"

	"github.com/satym-in/tenant-saas-backend/internal/config"
)

func TestConnectionDSNPrefersDatabaseURL(t *testing.T) {
	cfg := &config.Config{
		DatabaseURL: "postgresql://tenant:password@render-postgres:5432/tenant_saas",
		DBHost:      "should-not-be-used",
	}

	if got, want := connectionDSN(cfg), cfg.DatabaseURL; got != want {
		t.Fatalf("connectionDSN() = %q, want DATABASE_URL %q", got, want)
	}
}

func TestConnectionDSNFallsBackToSplitConfig(t *testing.T) {
	cfg := &config.Config{
		DBHost:     "postgres",
		DBPort:     "5432",
		DBUser:     "tenant",
		DBPassword: "secret",
		DBName:     "tenant_saas",
		DBSSLMode:  "disable",
	}

	want := "host=postgres port=5432 user=tenant password=secret dbname=tenant_saas sslmode=disable"
	if got := connectionDSN(cfg); got != want {
		t.Fatalf("connectionDSN() = %q, want %q", got, want)
	}
}
