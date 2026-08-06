package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/internal/db"
	"github.com/satym-in/tenant-saas-backend/internal/middleware"
	"github.com/satym-in/tenant-saas-backend/internal/routes"
)

func main() {
	cfg := config.Load()
	logger := middleware.NewLogger(cfg.Environment)
	slog.SetDefault(logger)

	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", slog.Any("error", err))
		os.Exit(1)
	}

	database, err := db.Connect(cfg)
	if err != nil {
		logger.Error("failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}

	if err := db.MigrateAtStartup(cfg, database, logger); err != nil {
		logger.Error("failed to run migrations", slog.Any("error", err))
		os.Exit(1)
	}

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: routes.Setup(database, cfg),
		// Timeouts are set explicitly because net/http's defaults are all
		// "no timeout", which lets a slow or malicious client hold a
		// connection (and a goroutine) open indefinitely -- a trivial
		// resource-exhaustion vector.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Run the listener on its own goroutine so main can block on signal
	// handling and coordinate an orderly shutdown.
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server listening",
			slog.String("addr", server.Addr),
			slog.String("environment", cfg.Environment))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	// SIGTERM is what container orchestrators (Kubernetes, ECS, Render) send
	// before killing a task. Handling it is what makes zero-downtime deploys
	// possible: in-flight requests finish instead of being severed mid-response.
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		logger.Error("server failed", slog.Any("error", err))
		os.Exit(1)

	case sig := <-shutdown:
		logger.Info("shutdown initiated", slog.String("signal", sig.String()))

		ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			// Graceful drain exceeded its budget; force the listener closed so
			// the process still exits rather than hanging forever.
			logger.Error("graceful shutdown timed out, forcing close", slog.Any("error", err))
			if closeErr := server.Close(); closeErr != nil {
				logger.Error("forced close failed", slog.Any("error", closeErr))
			}
		}

		if sqlDB, err := database.DB(); err == nil {
			if err := sqlDB.Close(); err != nil {
				logger.Error("failed to close database pool", slog.Any("error", err))
			}
		}

		logger.Info("shutdown complete")
	}
}
