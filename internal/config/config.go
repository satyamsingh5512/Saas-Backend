package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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

	// CORSAllowedOrigins is an explicit allow-list of browser origins
	// permitted to call the API cross-origin. Empty (the default) disables
	// CORS entirely, which is correct while the dashboard is served from the
	// same origin as the API.
	CORSAllowedOrigins []string

	// Connection pool sizing. Defaults are tuned for a small managed Postgres
	// instance: exceeding the server's max_connections is a far more common
	// production outage than pool starvation, so MaxOpenConns stays modest and
	// must be raised deliberately per deployment.
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration
	DBConnMaxIdleTime time.Duration

	// ShutdownTimeout bounds how long in-flight requests may finish during a
	// graceful shutdown before the process exits anyway.
	ShutdownTimeout time.Duration

	// Token lifetimes for the refresh-token-rotation auth model
	// (internal/identity). Parsed as Go durations (e.g. "15m", "720h").
	AccessTokenTTL       string
	RefreshTokenTTL      string
	PasswordResetTTL     string
	EmailVerificationTTL string

	// InvitationTTL bounds how long a member invite token stays redeemable. Kept
	// short by default because the token grants organization access and typically
	// sits in an inbox.
	InvitationTTL string

	// OAuth2 provider credentials (Phase 6). Empty values disable that
	// provider's login route rather than erroring, so the server remains
	// usable in environments that haven't configured OAuth yet.
	GoogleOAuthClientID     string
	GoogleOAuthClientSecret string
	GoogleOAuthRedirectURL  string
	GitHubOAuthClientID     string
	GitHubOAuthClientSecret string
	GitHubOAuthRedirectURL  string

	// Redis (Phase 11). Empty RedisAddr disables caching gracefully.
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// Kafka (Phase 10). Empty KafkaBrokers disables event publishing
	// gracefully (falls back to a no-op publisher).
	KafkaBrokers string

	// S3/MinIO object storage (Phase 12).
	S3Endpoint        string
	S3Region          string
	S3Bucket          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3UseSSL          bool
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

		CORSAllowedOrigins: getEnvList("CORS_ALLOWED_ORIGINS"),

		DBMaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", 25),
		DBMaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", 5),
		DBConnMaxLifetime: getEnvDuration("DB_CONN_MAX_LIFETIME", time.Hour),
		DBConnMaxIdleTime: getEnvDuration("DB_CONN_MAX_IDLE_TIME", 10*time.Minute),

		ShutdownTimeout: getEnvDuration("SHUTDOWN_TIMEOUT", 15*time.Second),

		AccessTokenTTL:       getEnv("ACCESS_TOKEN_TTL", "15m"),
		RefreshTokenTTL:      getEnv("REFRESH_TOKEN_TTL", "720h"), // 30 days
		PasswordResetTTL:     getEnv("PASSWORD_RESET_TTL", "1h"),
		EmailVerificationTTL: getEnv("EMAIL_VERIFICATION_TTL", "24h"),
		InvitationTTL:        getEnv("INVITATION_TTL", "168h"), // 7 days

		GoogleOAuthClientID:     getEnv("GOOGLE_OAUTH_CLIENT_ID", ""),
		GoogleOAuthClientSecret: getEnv("GOOGLE_OAUTH_CLIENT_SECRET", ""),
		GoogleOAuthRedirectURL:  getEnv("GOOGLE_OAUTH_REDIRECT_URL", ""),
		GitHubOAuthClientID:     getEnv("GITHUB_OAUTH_CLIENT_ID", ""),
		GitHubOAuthClientSecret: getEnv("GITHUB_OAUTH_CLIENT_SECRET", ""),
		GitHubOAuthRedirectURL:  getEnv("GITHUB_OAUTH_REDIRECT_URL", ""),

		RedisAddr:     getEnv("REDIS_ADDR", ""),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RedisDB:       getEnvInt("REDIS_DB", 0),

		KafkaBrokers: getEnv("KAFKA_BROKERS", ""),

		S3Endpoint:        getEnv("S3_ENDPOINT", ""),
		S3Region:          getEnv("S3_REGION", "us-east-1"),
		S3Bucket:          getEnv("S3_BUCKET", ""),
		S3AccessKeyID:     getEnv("S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey: getEnv("S3_SECRET_ACCESS_KEY", ""),
		S3UseSSL:          getEnvBool("S3_USE_SSL", true),
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

// ApplyMigrationOverrides mutates the config in place to use
// MIGRATE_DATABASE_URL / MIGRATE_DB_* environment variables in place of the
// app's normal runtime DATABASE_URL / DB_* values, when present. Intended
// for use by cmd/migrate only: migrations need elevated Postgres privileges
// (CREATE TABLE, CREATE POLICY, and CREATE ROLE for the app-role
// provisioning script) that the application's runtime credential
// deliberately does not have once app_user is provisioned (see
// scripts/provision_app_role.sql). Falls back to the normal DB_* values
// when no MIGRATE_* override is set, so local dev works with a single
// superuser credential before app_user exists yet.
func (c *Config) ApplyMigrationOverrides() {
	if v := strings.TrimSpace(os.Getenv("MIGRATE_DATABASE_URL")); v != "" {
		c.DatabaseURL = v
		return
	}
	if v := os.Getenv("MIGRATE_DB_USER"); v != "" {
		c.DatabaseURL = "" // ensure split DB_* fields take precedence below
		c.DBUser = v
	}
	if v := os.Getenv("MIGRATE_DB_PASSWORD"); v != "" {
		c.DBPassword = v
	}
	if v := os.Getenv("MIGRATE_DB_HOST"); v != "" {
		c.DBHost = v
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return b
}

// getEnvList parses a comma-separated environment variable into a slice,
// trimming whitespace and dropping empty entries. Returns nil when unset, which
// callers treat as "feature disabled" rather than "allow everything".
func getEnvList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// getEnvDuration parses a Go duration string (e.g. "30s", "1h"), falling back
// to the default on unset or malformed input so a typo degrades to a safe value
// instead of failing startup.
func getEnvDuration(key string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return d
}

// Duration parses one of the *_TTL config fields, returning fallback on
// empty/invalid input rather than erroring, so a misconfigured TTL degrades
// to a safe default instead of crashing startup.
func Duration(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return d
}
