package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/internal/db"
	"github.com/satym-in/tenant-saas-backend/internal/middleware"
	"github.com/satym-in/tenant-saas-backend/internal/platform/keepalive"
	"github.com/satym-in/tenant-saas-backend/internal/routes"
)

// runHealthcheck probes the local /health endpoint and exits 0/1. It exists
// for the Docker HEALTHCHECK in a distroless runtime image: distroless ships
// no shell, wget, or curl, so a SHELL-form probe is impossible and an exec
// probe needs a binary that speaks HTTP. The app binary is that binary:
// `HEALTHCHECK CMD ["/tenant-saas", "-healthcheck"]`.
func runHealthcheck() int {
	check := flag.Bool("healthcheck", false, "probe the local /health endpoint and exit")
	flag.Parse()
	if !*check {
		return -1 // not a healthcheck invocation; run the server normally
	}
	cfg := config.Load()
	port := cfg.Port
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/health", port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: "+err.Error())
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: unexpected status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

func main() {
	if code := runHealthcheck(); code >= 0 {
		os.Exit(code)
	}
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

	// Free-tier hosting spins down without inbound traffic and managed
	// databases reap idle pools; the keep-alive pings both on a ticker.
	// Stopped first on shutdown so it never races the pool close below.
	var stopKeepalive func()
	if cfg.KeepaliveEnabled {
		stopKeepalive = keepalive.Start(database, cfg.AppBaseURL, cfg.KeepaliveInterval, logger)
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

		if stopKeepalive != nil {
			stopKeepalive()
		}

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
