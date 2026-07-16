package main

import (
	"log"

	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/internal/db"
	"github.com/satym-in/tenant-saas-backend/internal/routes"
)

func main() {
	cfg := config.Load()

	database, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}

	if err := db.AutoMigrate(database); err != nil {
		log.Fatalf("failed to run migrations: %v", err)
	}

	router := routes.Setup(database, cfg)

	log.Printf("starting server on port %s", cfg.Port)
	if err := router.Run(":" + cfg.Port); err != nil {
		log.Fatalf("server failed to start: %v", err)
	}
}
