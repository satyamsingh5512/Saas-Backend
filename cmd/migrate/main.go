// Command migrate applies (or rolls back) versioned SQL migrations against
// the configured database. It exists as a separate entrypoint from the API
// server so migrations can be run as a distinct CI/CD or deployment step
// (e.g. a Kubernetes Job / init container) rather than implicitly on every
// server boot, which is unsafe for multi-replica deployments (N replicas
// starting simultaneously would race to apply migrations).
//
// Usage:
//
//	go run ./cmd/migrate up
//	go run ./cmd/migrate down 1
//	go run ./cmd/migrate version
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"

	gomigrate "github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/satym-in/tenant-saas-backend/internal/config"
	appdb "github.com/satym-in/tenant-saas-backend/internal/db"
	"github.com/satym-in/tenant-saas-backend/migrations"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: migrate <up|down|version> [steps]")
	}

	// Migrations require elevated privileges (CREATE TABLE, CREATE POLICY,
	// CREATE ROLE for scripts/provision_app_role.sql, etc.) that the
	// low-privilege app_user role intentionally does not have (see
	// migrations/000009's schema comment and scripts/provision_app_role.sql).
	// This tool therefore prefers MIGRATE_DATABASE_URL / MIGRATE_DB_* env
	// vars (superuser credentials) over the app's runtime DB_* config, so a
	// single .env file can safely configure the server to run as app_user
	// while migrations still run with the privileges they need.
	cfg := config.Load()
	cfg.ApplyMigrationOverrides()

	database, err := appdb.Connect(cfg)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	sqlDB, err := database.DB()
	if err != nil {
		log.Fatalf("get underlying sql.DB: %v", err)
	}
	defer sqlDB.Close()

	sourceDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		log.Fatalf("load embedded migrations: %v", err)
	}

	dbDriver, err := postgres.WithInstance(sqlDB, &postgres.Config{})
	if err != nil {
		log.Fatalf("init migration driver: %v", err)
	}

	m, err := gomigrate.NewWithInstance("iofs", sourceDriver, "postgres", dbDriver)
	if err != nil {
		log.Fatalf("init migrator: %v", err)
	}

	switch os.Args[1] {
	case "up":
		if err := m.Up(); err != nil && !errors.Is(err, gomigrate.ErrNoChange) {
			log.Fatalf("migrate up: %v", err)
		}
		fmt.Println("migrations applied")
	case "down":
		steps := 1
		if len(os.Args) > 2 {
			steps, err = strconv.Atoi(os.Args[2])
			if err != nil {
				log.Fatalf("invalid steps argument: %v", err)
			}
		}
		if err := m.Steps(-steps); err != nil && !errors.Is(err, gomigrate.ErrNoChange) {
			log.Fatalf("migrate down: %v", err)
		}
		fmt.Println("migrations reverted")
	case "version":
		version, dirty, err := m.Version()
		if err != nil {
			log.Fatalf("get version: %v", err)
		}
		fmt.Printf("version=%d dirty=%t\n", version, dirty)
	default:
		log.Fatalf("unknown command %q (expected up|down|version)", os.Args[1])
	}
}
