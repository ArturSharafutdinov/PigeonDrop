package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port             int
	StorageDir       string
	DefaultServerURL string
}

func Load() (Config, error) {
	config := Config{}
	port, err := strconv.Atoi(getEnv("PORT", "3000"))
	if err != nil {
		return Config{}, fmt.Errorf("error while parsing port: %w", err)
	}

	if !isPortValid(port) {
		return Config{}, fmt.Errorf("invalid port number: %d", port)
	}

	config.Port = port

	path, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("error while getting working directory: %w", err)
	}

	config.StorageDir = getEnv("STORAGE_DIR", path)
	config.DefaultServerURL = getEnv("DEFAULT_SERVER_URL", fmt.Sprintf("http://localhost:%d", port))

	return config, nil
}

func isPortValid(port int) bool {
	return port >= 1 && port <= 65535
}

func getEnv(name, defaultValue string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return defaultValue
}
