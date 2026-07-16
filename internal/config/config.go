package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	Port         string
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
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, relying on environment variables")
	}

	cfg := &Config{
		Port:         getEnv("PORT", "8080"),
		DBHost:       getEnv("DB_HOST", "localhost"),
		DBPort:       getEnv("DB_PORT", "5432"),
		DBUser:       getEnv("DB_USER", "postgres"),
		DBPassword:   getEnv("DB_PASSWORD", ""),
		DBName:       getEnv("DB_NAME", "tenant_saas"),
		DBSSLMode:    getEnv("DB_SSLMODE", "disable"),
		JWTSecret:    getEnv("JWT_SECRET", ""),
		JWTExpiryHrs: getEnv("JWT_EXPIRY_HOURS", "24"),
		Environment:  getEnv("APP_ENV", "development"),
	}

	if cfg.JWTSecret == "" {
		log.Println("WARNING: JWT_SECRET is not set. Set it via environment variable or .env file before running in production.")
	}

	return cfg
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
