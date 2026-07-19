package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

const minimumJWTSecretLength = 32

// Config holds all application configuration loaded from environment variables.
type Config struct {
	Port         string
	DatabaseURL  string
	DBHost       string
	DBPort       string
	DBUser       string
	DBPassword   string
	DBName       string
	DBSSLMode    string
	JWTSecret    string
	JWTExpiryHrs string
	Environment  string
}

// Load reads configuration from a .env file (if present) and environment variables.
// Environment variables always take precedence over .env file values.
func Load() *Config {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Printf("failed to load .env file: %v", err)
	}

	return &Config{
		Port:         getEnv("PORT", "8080"),
		DatabaseURL:  strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DBHost:       getEnv("DB_HOST", "localhost"),
		DBPort:       getEnv("DB_PORT", "5432"),
		DBUser:       getEnv("DB_USER", "postgres"),
		DBPassword:   getEnv("DB_PASSWORD", ""),
		DBName:       getEnv("DB_NAME", "tenant_saas"),
		DBSSLMode:    getEnv("DB_SSLMODE", "disable"),
		JWTSecret:    strings.TrimSpace(os.Getenv("JWT_SECRET")),
		JWTExpiryHrs: getEnv("JWT_EXPIRY_HOURS", "24"),
		Environment:  getEnv("APP_ENV", "development"),
	}
}

// Validate prevents unsafe or incomplete production configuration from starting.
// DATABASE_URL is preferred for managed providers such as Render. Split DB_* values
// remain supported for Docker Compose and other deployment targets.
func (c *Config) Validate() error {
	if !strings.EqualFold(c.Environment, "production") {
		return nil
	}

	if len(c.JWTSecret) < minimumJWTSecretLength {
		return fmt.Errorf("JWT_SECRET must be set to at least %d characters in production", minimumJWTSecretLength)
	}

	if c.DatabaseURL != "" {
		databaseURL, err := url.Parse(c.DatabaseURL)
		if err != nil || databaseURL.Host == "" || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") {
			return fmt.Errorf("DATABASE_URL must be a valid postgres:// or postgresql:// connection URL")
		}
		return nil
	}

	if c.DBHost == "" || c.DBHost == "localhost" {
		return fmt.Errorf("DATABASE_URL is required in production, or configure a non-local DB_HOST with DB_USER, DB_PASSWORD, and DB_NAME")
	}
	if c.DBUser == "" || c.DBPassword == "" || c.DBName == "" {
		return fmt.Errorf("DB_USER, DB_PASSWORD, and DB_NAME are required when DATABASE_URL is not set in production")
	}

	return nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
