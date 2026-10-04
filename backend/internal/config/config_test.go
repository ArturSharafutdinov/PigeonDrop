package config

import (
	"log/slog"
	"reflect"
	"strconv"
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
		c.MigrateOnStart || c.LogLevel != slog.LevelInfo || c.MaxDBConns != 10 ||
		!c.CookieSecure || c.TrustProxyHeaders || len(c.AllowedOrigins) != 0 || c.AccountStorageBytes != 100*1024*1024 {
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
		"DATABASE_URL":          " postgres://u:p@db/pigeon ",
		"PORT":                  "4000",
		"STORAGE_DIR":           "/data",
		"DEFAULT_SERVER_URL":    "https://pigeon.example.com/",
		"MIGRATE_ON_START":      "true",
		"LOG_LEVEL":             "debug",
		"MAX_DB_CONNS":          "5",
		"COOKIE_SECURE":         "false",
		"TRUST_PROXY_HEADERS":   "true",
		"ALLOWED_ORIGINS":       " http://localhost:5173, https://app.example.com/ ,",
		"STORAGE_ACCOUNT_BYTES": " 2000000 ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		DatabaseURL: "postgres://u:p@db/pigeon", Port: 4000, StorageDir: "/data",
		DefaultServerURL: "https://pigeon.example.com", MigrateOnStart: true, LogLevel: slog.LevelDebug, MaxDBConns: 5,
		CookieSecure: false, TrustProxyHeaders: true, AllowedOrigins: []string{"http://localhost:5173", "https://app.example.com"},
		AccountStorageBytes: 2000000,
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{
		"PORT":                  "99999",
		"DEFAULT_SERVER_URL":    "ftp://nope",
		"MIGRATE_ON_START":      "maybe",
		"LOG_LEVEL":             "loud",
		"MAX_DB_CONNS":          "0",
		"COOKIE_SECURE":         "sometimes",
		"TRUST_PROXY_HEADERS":   "yes please",
		"ALLOWED_ORIGINS":       "https://ok.example.com,not-an-origin,https://app.example.com/path",
		"STORAGE_ACCOUNT_BYTES": "100MB",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, part := range []string{"DATABASE_URL is required", "PORT must", "DEFAULT_SERVER_URL must", "MIGRATE_ON_START must", "LOG_LEVEL must", "MAX_DB_CONNS must",
		"COOKIE_SECURE must", "TRUST_PROXY_HEADERS must", `STORAGE_ACCOUNT_BYTES must be a number of bytes, at least 100000, got "100MB"`, `ALLOWED_ORIGINS must list origins like https://app.example.com, got "not-an-origin"`,
		`got "https://app.example.com/path"`} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error does not mention %q:\n%v", part, err)
		}
	}
}

func TestStorageAccountBytesHasASaneFloor(t *testing.T) {
	for _, v := range []string{"0", "-5", "99999", "1e9", "1 000", "100MB"} {
		if _, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "STORAGE_ACCOUNT_BYTES": v})); err == nil {
			t.Errorf("%q was accepted", v)
		}
	}
	for _, v := range []string{"100000", "300000", "104857600"} {
		if c, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "STORAGE_ACCOUNT_BYTES": v})); err != nil || strconv.FormatInt(c.AccountStorageBytes, 10) != v {
			t.Errorf("%q: %+v %v", v, c, err)
		}
	}
}
