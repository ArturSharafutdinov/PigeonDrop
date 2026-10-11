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
	// The Telegram bot is off until it has a token.
	if c.TelegramToken != "" || c.TelegramAPIURL != "" || c.TelegramWait != 3*time.Minute || c.TelegramTimeout != 30*time.Second {
		t.Fatalf("unexpected Telegram defaults: %+v", c)
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
		"TELEGRAM_BOT_TOKEN":    " 123456789:AAH-test_token0123456789abcdefghijk ",
		"TELEGRAM_API_URL":      " http://localhost:8081/ ",
		"TELEGRAM_WAIT":         "90s",
		"TELEGRAM_TIMEOUT":      "10s",
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
		TelegramToken:  "123456789:AAH-test_token0123456789abcdefghijk", TelegramAPIURL: "http://localhost:8081",
		TelegramWait: 90 * time.Second, TelegramTimeout: 10 * time.Second,
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

func TestTelegramSettingsHaveLimits(t *testing.T) {
	const token = "123456789:AAH-test_token0123456789abcdefghijk"
	bad := map[string][]string{
		"TELEGRAM_BOT_TOKEN": {"nonsense", "123456789", "123456789:short", ":AAH-test_token0123456789abcdefghijk", "abc:AAH-test_token0123456789abcdefghijk", "123456789:AAH test token0123456789abcdef", "123456789:AAH/test_token0123456789abcdef"},
		"TELEGRAM_API_URL":   {"api.telegram.org", "ftp://x", "http://", "https://u:p@x", "https://x/bot", "https://x?a=1"},
		"TELEGRAM_WAIT":      {"0s", "500ms", "31m", "3", "-1m", "soon"},
		"TELEGRAM_TIMEOUT":   {"0s", "500ms", "6m", "30", "-1m", "soon"},
	}
	for name, values := range bad {
		for _, v := range values {
			vars := map[string]string{"DATABASE_URL": "postgres://x/y", "TELEGRAM_BOT_TOKEN": token, name: v}
			_, err := Load(env(vars))
			if err == nil {
				t.Errorf("%s=%q was accepted", name, v)
				continue
			}
			if name == "TELEGRAM_BOT_TOKEN" && v != "" && v != "nonsense" && strings.Contains(err.Error(), v) {
				t.Errorf("the wrong token is repeated in the error: %v", err)
			}
		}
	}
	good := map[string]string{"TELEGRAM_WAIT": "1s", "TELEGRAM_TIMEOUT": "5m", "TELEGRAM_API_URL": "https://tg.example.com:8443"}
	for name, v := range good {
		if _, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "TELEGRAM_BOT_TOKEN": token, name: v})); err != nil {
			t.Errorf("%s=%q: %v", name, v, err)
		}
	}
	if _, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "TELEGRAM_WAIT": "30m"})); err != nil {
		t.Errorf("a wait without a token (the bot is off) must load: %v", err)
	}
}

func TestTelegramAddressWithoutATokenIsAMistake(t *testing.T) {
	// An address with no token usually means the token was lost, and the bot would silently stay off.
	_, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "TELEGRAM_API_URL": "http://localhost:8081"}))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_API_URL is set but TELEGRAM_BOT_TOKEN is not") {
		t.Fatalf("err = %v", err)
	}
}

func TestAWrongTelegramTokenIsNeverEchoed(t *testing.T) {
	const wrong = "999999999:this-one-has-a-space in it-and-more"
	_, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x/y", "TELEGRAM_BOT_TOKEN": wrong}))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN does not look like a token") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "999999999") || strings.Contains(err.Error(), "this-one") {
		t.Errorf("the token is in the error: %v", err)
	}
}
