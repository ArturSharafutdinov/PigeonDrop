package config

import (
	"fmt"
	"os"
)

type Config struct {
	DatabaseURL string
}

func Load() (Config, error) {
	dbUrl := os.Getenv("DATABASE_URL")
	if dbUrl == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	return Config{DatabaseURL: dbUrl}, nil
}
