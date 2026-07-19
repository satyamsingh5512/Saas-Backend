package config

import (
	"strings"
	"testing"
)

func TestValidateProductionAcceptsRenderDatabaseURL(t *testing.T) {
	cfg := &Config{
		Environment: "production",
		JWTSecret:   strings.Repeat("s", minimumJWTSecretLength),
		DatabaseURL: "postgresql://tenant:password@dpg-example:5432/tenant_saas",
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
		Environment: "production",
		JWTSecret:   strings.Repeat("s", minimumJWTSecretLength),
		DBHost:      "postgres.internal",
		DBUser:      "tenant",
		DBPassword:  "password",
		DBName:      "tenant_saas",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() returned an unexpected error: %v", err)
	}
}
