package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/asharafutdinov/pigeondrop/internal/config"
	"github.com/asharafutdinov/pigeondrop/internal/handler"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	c, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, c.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return err
	}

	router := handler.NewRouter(func() error {
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return pool.Ping(pingCtx)
	})

	server := &http.Server{Addr: c.ServerURL, Handler: router, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	serverErr := make(chan error, 1)

	go func() {
		slog.Info("server starting", "addr", c.ServerURL)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// manual shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	select {
	case err := <-serverErr:
		return err
	case <-stop:
		slog.Info("shutting down...")
	}

	// graceful shutdown
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return server.Shutdown(shutdownContext)
}
