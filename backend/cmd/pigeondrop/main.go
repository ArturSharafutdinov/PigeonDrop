package main

import (
	"log/slog"
	"os"

	"github.com/asharafutdinov/pigeondrop/internal/config"
)

// TODO: создание папки storage
// TODO: полноценный логгер (.log в папке storage)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	c, err := config.Load()
	if err != nil {
		logger.Error("error while loading config", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("config created", slog.Any("config", c))
}
