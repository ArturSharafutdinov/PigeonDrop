package main

import (
	"log/slog"
	"os"

	"github.com/asharafutdinov/pigeondrop/internal/config"
)

// TODO: создание папки storage
// TODO: полноценный логгер (.log в папке storage)

func init() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
}

func main() {
	c, err := config.Load()
	if err != nil {
		slog.Error("error while loading config", slog.Any("error", err))
		os.Exit(1)
	}

	slog.Info("config created", slog.Any("config", c))
}
