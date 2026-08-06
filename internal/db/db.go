package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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
	// Development logs every statement. Other environments log only slow
	// queries and errors: Silent hides the "SLOW SQL" warnings that are the
	// first sign of a mislocated database, which is a diagnostic worth keeping
	// in production, while full statement logging there would put tenant data
	// and token hashes into log storage on every request.
	gormLogger := logger.Default.LogMode(logger.Warn)
	if cfg.Environment == "development" {
		gormLogger = logger.Default.LogMode(logger.Info)
	}

	database, err := gorm.Open(gormpostgres.Open(connectionDSN(cfg)), &gorm.Config{
		Logger: gormLogger,
		// Cache the parsed/prepared form of every statement instead of
		// re-preparing it per execution. With the extended protocol a fresh
		// statement costs a describe round trip before the execute, so this
		// removes one network round trip from most queries -- worth having in
		// general, and worth a lot when the database is not local.
		//
		// This requires a direct PostgreSQL connection. Behind a transaction-mode
		// pooler (PgBouncer, Supabase's 6543 port) prepared statements do not
		// survive between statements and this must be turned off.
		PrepareStmt: true,
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

// DSN builds the PostgreSQL connection string for a config, preferring
// DATABASE_URL over the individual DB_* values. Exported so callers can tell
// whether a migration credential differs from the runtime one.
func DSN(cfg *config.Config) string {
	return connectionDSN(cfg)
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

// MigrateAtStartup applies pending migrations before the server begins serving.
//
// It deliberately does not reuse the runtime connection when a MIGRATE_*
// credential is configured. The runtime role is intentionally powerless:
// NOSUPERUSER, NOBYPASSRLS, and holding no CREATE TABLE or CREATE POLICY grant,
// because those are exactly the privileges that would let it ignore tenant
// isolation. Migrations need all of them. Running both through one credential
// forces a choice between "migrations work" and "RLS is enforced", and the
// version that silently picks the first is the one that leaks tenant data.
//
// So when MIGRATE_DATABASE_URL (or MIGRATE_DB_*) differs from the runtime
// credential, this opens a second short-lived pool for the migration and closes
// it before serving starts. With no override configured it falls back to the
// runtime connection, which keeps local development on a single superuser
// credential working exactly as before.
func MigrateAtStartup(cfg *config.Config, runtime *gorm.DB, log *slog.Logger) error {
	migrateCfg := *cfg
	migrateCfg.ApplyMigrationOverrides()

	if DSN(&migrateCfg) == DSN(cfg) {
		return Migrate(runtime)
	}

	log.Info("applying migrations with the dedicated migration credential")

	migrateDB, err := Connect(&migrateCfg)
	if err != nil {
		return fmt.Errorf("connect with migration credential: %w", err)
	}
	defer func() {
		if sqlDB, dbErr := migrateDB.DB(); dbErr == nil {
			if closeErr := sqlDB.Close(); closeErr != nil {
				log.Error("failed to close migration pool", slog.Any("error", closeErr))
			}
		}
	}()

	return Migrate(migrateDB)
}
func Migrate(database *gorm.DB) error {
	sqlDB, err := database.DB()
	if err != nil {
		return fmt.Errorf("get underlying sql.DB: %w", err)
	}
	return RunMigrations(sqlDB)
}
