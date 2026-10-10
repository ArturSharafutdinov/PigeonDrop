package config

import (
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
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
	// Speech recognition is off until the service is named, and has its defaults ready for when it is.
	if c.SpeechURL != "" || c.SpeechToken != "" || c.SpeechTimeout != 90*time.Second || c.SpeechMaxAttempts != 5 || c.SpeechMaxAudio != 65*time.Second {
		t.Fatalf("unexpected speech recognition defaults: %+v", c)
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
		"STT_URL":               " http://stt:8000/ ",
		"STT_TOKEN":             " shared-secret ",
		"STT_TIMEOUT":           "2m",
		"STT_MAX_ATTEMPTS":      "3",
		"STT_MAX_AUDIO_SECONDS": "30.5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		DatabaseURL: "postgres://u:p@db/pigeon", Port: 4000, StorageDir: "/data",
		DefaultServerURL: "https://pigeon.example.com", MigrateOnStart: true, LogLevel: slog.LevelDebug, MaxDBConns: 5,
		CookieSecure: false, TrustProxyHeaders: true, AllowedOrigins: []string{"http://localhost:5173", "https://app.example.com"},
		AccountStorageBytes: 2000000,
		SpeechURL:           "http://stt:8000", SpeechToken: "shared-secret", SpeechTimeout: 2 * time.Minute, SpeechMaxAttempts: 3,
		SpeechMaxAudio: 30*time.Second + 500*time.Millisecond,
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
		"STT_URL":               "stt:8000",
		"STT_TIMEOUT":           "forever",
		"STT_MAX_ATTEMPTS":      "0",
		"STT_MAX_AUDIO_SECONDS": "-3",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, part := range []string{"DATABASE_URL is required", "PORT must", "DEFAULT_SERVER_URL must", "MIGRATE_ON_START must", "LOG_LEVEL must", "MAX_DB_CONNS must",
		"COOKIE_SECURE must", "TRUST_PROXY_HEADERS must", `STORAGE_ACCOUNT_BYTES must be a number of bytes, at least 100000, got "100MB"`, `ALLOWED_ORIGINS must list origins like https://app.example.com, got "not-an-origin"`,
		`got "https://app.example.com/path"`, "STT_URL must", "STT_TIMEOUT must", "STT_MAX_ATTEMPTS must", "STT_MAX_AUDIO_SECONDS must"} {
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

func TestSpeechRecognitionSettingsHaveLimits(t *testing.T) {
	bad := map[string][]string{
		"STT_URL":               {"stt", "ftp://stt", "http://", "://x"},
		"STT_TIMEOUT":           {"0s", "500ms", "11m", "90", "-1m", "soon"},
		"STT_MAX_ATTEMPTS":      {"0", "21", "-1", "two", "1.5"},
		"STT_MAX_AUDIO_SECONDS": {"0", "0.5", "601", "-5", "long"},
	}
	for name, values := range bad {
		for _, v := range values {
			if _, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "STT_URL": "http://stt:8000", name: v})); err == nil {
				t.Errorf("%s=%q was accepted", name, v)
			}
		}
	}
	good := map[string]string{"STT_TIMEOUT": "1s", "STT_MAX_ATTEMPTS": "20", "STT_MAX_AUDIO_SECONDS": "600"}
	for name, v := range good {
		if _, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "STT_URL": "http://stt:8000", name: v})); err != nil {
			t.Errorf("%s=%q: %v", name, v, err)
		}
	}
}

func TestSpeechTokenWithoutAServiceIsAMistake(t *testing.T) {
	// A token with no address usually means STT_URL was lost, and recognition would silently stay off.
	_, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "STT_TOKEN": "secret"}))
	if err == nil || !strings.Contains(err.Error(), "STT_TOKEN is set but STT_URL is not") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "STT_URL": "http://stt:8000"})); err != nil {
		t.Errorf("a service without a token (started for experiments) must load: %v", err)
	}
}
