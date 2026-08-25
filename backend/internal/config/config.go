package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	// database settings
	DatabaseURL string

	// server settings
	ServerURL    string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

func getEnvOrDefault[T any](key string, defaultValue T) (T, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}

	var empty T
	switch any(empty).(type) {
	case int:
		n, err := strconv.Atoi(value)
		if err != nil {
			return empty, err
		}
		return any(n).(T), nil
	case string:
		return any(value).(T), nil
	}

	return empty, fmt.Errorf("unsupported type")
}

func Load() (Config, error) {
	dbUrl := os.Getenv("DATABASE_URL")
	if dbUrl == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	serverUrl, _ := getEnvOrDefault[string]("SERVER_URL", ":3000")
	readTimeout, _ := getEnvOrDefault[int]("READ_TIMEOUT", 10)
	writeTimeout, _ := getEnvOrDefault[int]("WRITE_TIMEOUT", 10)

	return Config{DatabaseURL: dbUrl, ServerURL: serverUrl, ReadTimeout: time.Duration(readTimeout) * time.Second, WriteTimeout: time.Duration(writeTimeout) * time.Second}, nil
}
