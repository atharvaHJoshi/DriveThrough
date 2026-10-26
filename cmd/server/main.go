// Command server runs the DriveThrough HTTP API.
//
// Every external resource is created here and handed to the application, so
// the process lifecycle — connect, serve, drain, close — is visible in one file.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"drivethrough/internal/app"
	"drivethrough/internal/auth"
	"drivethrough/internal/config"
	"drivethrough/internal/database"
	"drivethrough/internal/websocket"
	"drivethrough/pkg/logger"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger.New(cfg.Log.Level, cfg.Log.Format)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.NewPostgres(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer db.Close()

	if cfg.Database.RunMigrations {
		if err := database.RunMigrations(ctx, db); err != nil {
			return fmt.Errorf("run migrations: %w", err)
		}
	}

	rdb, err := database.NewRedis(ctx, cfg.Redis)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() { _ = rdb.Close() }()

	application := app.New(app.Deps{
		Cfg:   cfg,
		DB:    db,
		Redis: rdb,
		JWT:   auth.NewJWTManager(cfg.JWT, auth.NewTokenBlacklist(rdb)),
		Hub:   websocket.NewHub(cfg.WS),
	})

	// The same ctx that receives SIGTERM also drives every background worker,
	// so shutdown stops the loops before the HTTP server drains.
	application.Start(ctx)

	server := &http.Server{
		Addr:    ":" + cfg.Server.Port,
		Handler: application.Handler,
		// WebSocket routes hijack the connection, so the write deadline must
		// be disabled or a long-lived trip-tracking socket is cut off early.
		ReadHeaderTimeout: cfg.Server.ReadTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      0,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", server.Addr, "env", cfg.Server.Env)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining connections")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	slog.Info("server stopped cleanly", "at", time.Now().UTC())
	return nil
}
