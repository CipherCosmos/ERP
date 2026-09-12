// Command server runs the GBOS API.
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

	"gbos/internal/app"
	"gbos/internal/config"
	api "gbos/internal/http"
	platformlog "gbos/internal/platform/log"
	platform "gbos/internal/platform/postgres"

	"github.com/gin-gonic/gin"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := platformlog.New(platformlog.Options{
		Level:  cfg.LogLevel,
		Format: logFormat(cfg.GinMode),
	})
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Migrations run before the pool opens, so the application never serves a
	// request against a schema it does not understand. They are compiled into
	// this binary and golang-migrate holds an advisory lock, so two instances
	// starting together queue rather than racing.
	version, err := app.MigrateUp(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	logger.Info("schema ready", "version", version)

	db, err := platform.Open(ctx, platform.Config{DSN: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	boot, err := app.EnsureBootstrap(ctx, db)
	if err != nil {
		return err
	}
	logBootstrap(logger, boot)

	svcs, err := app.Build(db, app.Config{JWTSecret: cfg.JWTSecret})
	if err != nil {
		return err
	}

	gin.SetMode(cfg.GinMode)
	router, shutdownRouter, err := api.NewRouter(svcs, api.Config{
		CORSOrigin:     cfg.CORSOrigin,
		DB:             db,
		TrustedProxies: cfg.TrustedProxies,
	})
	if err != nil {
		return err
	}
	defer shutdownRouter()

	srv := &http.Server{
		Addr:              ":" + cfg.ServerPort,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "port", cfg.ServerPort, "mode", cfg.GinMode)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	// Give in-flight requests a chance to finish. A sale halfway through its
	// transaction should commit or roll back cleanly rather than being cut off.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("forced shutdown", "error", err)
	}
	logger.Info("stopped")
	return nil
}

func logFormat(ginMode string) string {
	if ginMode == "release" {
		return "json"
	}
	return "text"
}

// logBootstrap prints the generated owner password exactly once, since there
// is no other way to learn it and nothing stores it in the clear.
func logBootstrap(logger *slog.Logger, boot app.Bootstrap) {
	if !boot.Created {
		return
	}
	if boot.Generated {
		logger.Warn("first run: created the owner account",
			"username", boot.Username,
			"password", boot.Password,
			"note", "this is the only time this password is shown — sign in and change it now")
		return
	}
	logger.Info("first run: created the owner account",
		"username", boot.Username,
		"note", "the password came from GBOS_ADMIN_PASSWORD")
}
