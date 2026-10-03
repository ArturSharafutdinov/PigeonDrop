package config

import (
	"log/slog"
	"strings"
	"testing"
)

func env(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 3000 || c.StorageDir != "./data" || c.DefaultServerURL != "http://localhost:3000" ||
		c.MigrateOnStart || c.LogLevel != slog.LevelInfo || c.MaxDBConns != 10 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadDefaultServerURLFollowsPort(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "PORT": "8080"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultServerURL != "http://localhost:8080" {
		t.Fatalf("got %q", c.DefaultServerURL)
	}
}

func TestLoadValues(t *testing.T) {
	c, err := Load(env(map[string]string{
		"DATABASE_URL":       " postgres://u:p@db/pigeon ",
		"PORT":               "4000",
		"STORAGE_DIR":        "/data",
		"DEFAULT_SERVER_URL": "https://pigeon.example.com/",
		"MIGRATE_ON_START":   "true",
		"LOG_LEVEL":          "debug",
		"MAX_DB_CONNS":       "5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		DatabaseURL: "postgres://u:p@db/pigeon", Port: 4000, StorageDir: "/data",
		DefaultServerURL: "https://pigeon.example.com", MigrateOnStart: true, LogLevel: slog.LevelDebug, MaxDBConns: 5,
	}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{
		"PORT":               "99999",
		"DEFAULT_SERVER_URL": "ftp://nope",
		"MIGRATE_ON_START":   "maybe",
		"LOG_LEVEL":          "loud",
		"MAX_DB_CONNS":       "0",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, part := range []string{"DATABASE_URL is required", "PORT must", "DEFAULT_SERVER_URL must", "MIGRATE_ON_START must", "LOG_LEVEL must", "MAX_DB_CONNS must"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error does not mention %q:\n%v", part, err)
		}
	}
}
