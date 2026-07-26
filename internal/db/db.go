package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/migrations"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Connect opens a connection pool to PostgreSQL using DATABASE_URL when present,
// otherwise using the individual DB_* configuration values.
//
// Pool limits are applied explicitly rather than left at database/sql's
// defaults. The default MaxOpenConns is unlimited, which lets a traffic spike
// open connections until Postgres refuses them with "too many clients" --
// turning a slowdown into a hard outage. A bounded pool queues instead.
func Connect(cfg *config.Config) (*gorm.DB, error) {
	logLevel := logger.Silent
	if cfg.Environment == "development" {
		logLevel = logger.Info
	}

	database, err := gorm.Open(gormpostgres.Open(connectionDSN(cfg)), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})
	if err != nil {
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	sqlDB, err := database.DB()
	if err != nil {
		return nil, fmt.Errorf("get underlying sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
	// Recycling connections bounds the damage from a connection that has drifted
	// into a bad state, and lets managed-Postgres failovers/rolling restarts be
	// picked up without an application restart.
	sqlDB.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.DBConnMaxIdleTime)

	// Fail fast at startup on an unreachable database rather than surfacing the
	// problem as confusing per-request 500s after the process reports "ready".
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return database, nil
}

func connectionDSN(cfg *config.Config) string {
	if cfg.DatabaseURL != "" {
		return cfg.DatabaseURL
	}

	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName, cfg.DBSSLMode,
	)
}

// RunMigrations applies all pending versioned SQL migrations embedded in the
// migrations/ directory using golang-migrate. This replaces GORM AutoMigrate:
// AutoMigrate cannot express Row-Level Security policies, partial/composite
// indexes, CHECK constraints, or triggers, all of which the production schema
// requires for tenant isolation (see migrations/000002_tenants_and_users.up.sql).
func RunMigrations(sqlDB *sql.DB) error {
	sourceDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	dbDriver, err := postgres.WithInstance(sqlDB, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("init migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", dbDriver)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	return nil
}

// Migrate is a convenience wrapper that extracts the underlying *sql.DB from
// a *gorm.DB and applies all pending migrations. Callers that already have a
// raw *sql.DB (e.g. cmd/migrate) should call RunMigrations directly instead.
func Migrate(database *gorm.DB) error {
	sqlDB, err := database.DB()
	if err != nil {
		return fmt.Errorf("get underlying sql.DB: %w", err)
	}
	return RunMigrations(sqlDB)
}
