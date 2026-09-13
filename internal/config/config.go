package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const minimumJWTSecretLength = 32

// Config holds all application configuration loaded from environment variables.
type Config struct {
	Port        string
	DatabaseURL string
	DBHost      string
	DBPort      string
	DBUser      string
	DBPassword  string
	DBName      string
	DBSSLMode   string
	JWTSecret   string
	Environment string

	// CORSAllowedOrigins is an explicit allow-list of browser origins
	// permitted to call the API cross-origin. Empty (the default) disables
	// CORS entirely, which is correct while the dashboard is served from the
	// same origin as the API, or reached through a same-origin proxy.
	CORSAllowedOrigins []string

	// TenantBaseDomain is the apex under which tenant subdomains live, e.g.
	// "ourapp.com" so acme.ourapp.com resolves the "acme" tenant pre-auth.
	// Empty (the default) disables subdomain inference, leaving X-Tenant-ID and
	// the authenticated credential as the tenant sources.
	//
	// Leave it unset on a shared hosting domain such as *.onrender.com: the
	// service name is not a tenant slug, and treating it as one makes every
	// pre-auth lookup fail.
	TenantBaseDomain string

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

	// Keepalive keeps free-tier hosting and databases from idling out (see
	// internal/platform/keepalive). Enabled by default; set
	// KEEPALIVE_ENABLED=false to silence it. The interval floors at 30s:
	// anything more aggressive is load without benefit, since hosts measure
	// idleness in minutes.
	KeepaliveEnabled  bool
	KeepaliveInterval time.Duration

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

	// OAuth2 provider credentials. Empty values disable that provider's login
	// route rather than erroring, so the server remains usable in environments
	// that haven't configured OAuth yet.
	GitHubOAuthClientID     string
	GitHubOAuthClientSecret string
	GitHubOAuthRedirectURL  string

	// Outbound email via Resend. An empty ResendAPIKey selects the no-op
	// transport: token-bearing mail is logged as undeliverable rather than
	// silently dropped, and no flow fails for want of a mail provider.
	ResendAPIKey string
	// MailFrom is the envelope sender, e.g. "Acme <noreply@acme.com>". It must
	// be on a domain verified in Resend or the API rejects every send.
	MailFrom string
	// AppBaseURL is the public origin used to build links in outbound mail
	// (invite, password reset, email verification). Links are unusable without
	// it, so an empty value in production disables sending rather than mailing
	// a relative path.
	AppBaseURL string

	// File storage. Local storage is the development default; production may use
	// either a persistent local volume or an S3-compatible object store.
	StorageDriver  string
	StorageRoot    string
	MaxUploadBytes int64
	S3Endpoint     string
	S3Region       string
	S3Bucket       string
	S3AccessKey    string
	S3SecretKey    string
	S3UsePathStyle bool
	S3Secure       bool

	// Redis (Phase 11). Empty RedisAddr disables caching gracefully.
	RedisAddr     string
	RedisPassword string
	RedisDB       int
}

// Load reads configuration from a .env file (if present) and environment variables.
// Environment variables always take precedence over .env file values.
func Load() *Config {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Printf("failed to load .env file: %v", err)
	}

	return &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DBHost:      getEnv("DB_HOST", "localhost"),
		DBPort:      getEnv("DB_PORT", "5432"),
		DBUser:      getEnv("DB_USER", "postgres"),
		DBPassword:  getEnv("DB_PASSWORD", ""),
		DBName:      getEnv("DB_NAME", "tenant_saas"),
		DBSSLMode:   getEnv("DB_SSLMODE", "disable"),
		JWTSecret:   strings.TrimSpace(os.Getenv("JWT_SECRET")),
		Environment: getEnv("APP_ENV", "development"),

		CORSAllowedOrigins: getEnvList("CORS_ALLOWED_ORIGINS"),
		TenantBaseDomain:   strings.TrimSpace(os.Getenv("TENANT_BASE_DOMAIN")),

		DBMaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", 25),
		DBMaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", 5),
		DBConnMaxLifetime: getEnvDuration("DB_CONN_MAX_LIFETIME", time.Hour),
		DBConnMaxIdleTime: getEnvDuration("DB_CONN_MAX_IDLE_TIME", 10*time.Minute),

		ShutdownTimeout: getEnvDuration("SHUTDOWN_TIMEOUT", 15*time.Second),

		KeepaliveEnabled:  getEnvBool("KEEPALIVE_ENABLED", true),
		KeepaliveInterval: max(getEnvDuration("KEEPALIVE_INTERVAL", time.Minute), 30*time.Second),

		AccessTokenTTL:       getEnv("ACCESS_TOKEN_TTL", "15m"),
		RefreshTokenTTL:      getEnv("REFRESH_TOKEN_TTL", "720h"), // 30 days
		PasswordResetTTL:     getEnv("PASSWORD_RESET_TTL", "1h"),
		EmailVerificationTTL: getEnv("EMAIL_VERIFICATION_TTL", "24h"),
		InvitationTTL:        getEnv("INVITATION_TTL", "168h"), // 7 days

		GitHubOAuthClientID:     getEnv("GITHUB_OAUTH_CLIENT_ID", ""),
		GitHubOAuthClientSecret: getEnv("GITHUB_OAUTH_CLIENT_SECRET", ""),
		GitHubOAuthRedirectURL:  getEnv("GITHUB_OAUTH_REDIRECT_URL", ""),

		ResendAPIKey: strings.TrimSpace(os.Getenv("RESEND_API_KEY")),
		MailFrom:     strings.TrimSpace(os.Getenv("MAIL_FROM")),
		AppBaseURL:   strings.TrimRight(strings.TrimSpace(os.Getenv("APP_BASE_URL")), "/"),

		StorageDriver:  strings.ToLower(getEnv("STORAGE_DRIVER", "local")),
		StorageRoot:    getEnv("STORAGE_ROOT", "./data/uploads"),
		MaxUploadBytes: getEnvInt64("MAX_UPLOAD_BYTES", 25*1024*1024),
		S3Endpoint:     strings.TrimSpace(os.Getenv("S3_ENDPOINT")),
		S3Region:       getEnv("S3_REGION", "us-east-1"),
		S3Bucket:       strings.TrimSpace(os.Getenv("S3_BUCKET")),
		S3AccessKey:    strings.TrimSpace(os.Getenv("S3_ACCESS_KEY_ID")),
		S3SecretKey:    strings.TrimSpace(os.Getenv("S3_SECRET_ACCESS_KEY")),
		S3UsePathStyle: getEnvBool("S3_USE_PATH_STYLE", false),
		S3Secure:       getEnvBool("S3_SECURE", true),

		RedisAddr:     getEnv("REDIS_ADDR", ""),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RedisDB:       getEnvInt("REDIS_DB", 0),
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
		return c.ValidateStorage()
	}

	if c.DBHost == "" || c.DBHost == "localhost" {
		return fmt.Errorf("DATABASE_URL is required in production, or configure a non-local DB_HOST with DB_USER, DB_PASSWORD, and DB_NAME")
	}
	if c.DBUser == "" || c.DBPassword == "" || c.DBName == "" {
		return fmt.Errorf("DB_USER, DB_PASSWORD, and DB_NAME are required when DATABASE_URL is not set in production")
	}

	return c.ValidateStorage()
}

// ValidateStorage rejects a storage configuration that would silently make
// uploads unavailable or ephemeral in production.
func (c *Config) ValidateStorage() error {
	driver := strings.ToLower(strings.TrimSpace(c.StorageDriver))
	if driver == "" {
		driver = "local"
	}
	if driver != "local" && driver != "s3" {
		return fmt.Errorf("STORAGE_DRIVER must be local or s3")
	}
	if c.MaxUploadBytes <= 0 {
		return fmt.Errorf("MAX_UPLOAD_BYTES must be greater than zero")
	}
	if driver == "local" {
		if strings.EqualFold(c.Environment, "production") && (strings.TrimSpace(c.StorageRoot) == "" || !filepath.IsAbs(c.StorageRoot)) {
			return fmt.Errorf("STORAGE_ROOT must point to a persistent writable volume when STORAGE_DRIVER=local in production")
		}
		return nil
	}
	if c.S3Bucket == "" || c.S3AccessKey == "" || c.S3SecretKey == "" {
		return fmt.Errorf("S3_BUCKET, S3_ACCESS_KEY_ID, and S3_SECRET_ACCESS_KEY are required when STORAGE_DRIVER=s3")
	}
	if c.S3Region == "" {
		return fmt.Errorf("S3_REGION must be set when STORAGE_DRIVER=s3")
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

func getEnvInt64(key string, fallback int64) int64 {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}
	n, err := strconv.ParseInt(value, 10, 64)
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
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
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
