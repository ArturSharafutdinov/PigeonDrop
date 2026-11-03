// Package config reads the server's settings from environment variables, the way
// Coolify and Docker pass them in. Nothing is read from files.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// DatabaseURL is the PostgreSQL connection string (required).
	DatabaseURL string
	// Port the HTTP server listens on.
	Port int
	// StorageDir holds the recordings (and anything else that must persist as files).
	StorageDir string
	// DefaultServerURL is this server's own public address. Accounts resolve to it
	// unless they name a server of their own, so it must be reachable by the watch.
	DefaultServerURL string
	// MigrateOnStart applies pending migrations before serving. Fine for a single
	// instance; with several, run `pigeondrop migrate up` as a release step instead.
	MigrateOnStart bool
	LogLevel       slog.Level
	// MaxDBConns caps the connection pool: the PostgreSQL server is shared with other projects.
	MaxDBConns int32
	// CookieSecure marks the session cookie Secure (HTTPS only). Leave it on everywhere but local development
	// over plain http, where browsers would refuse to send the cookie back.
	CookieSecure bool
	// AllowedOrigins are web pages, besides this server's own address, that the API's cross-origin check (the CSRF
	// guard) lets send requests with a signed-in session - for example the front end's dev server. Each is
	// scheme://host[:port]. It feeds that check and nothing else: the server sends no CORS headers
	// (Access-Control-*), so listing a page here does not let its scripts read the answers; for that the front end
	// has to be served from the server's own address, through a proxy.
	AllowedOrigins []string
	// TrustProxyHeaders believes X-Forwarded-For for the client's address (the sign-in limits are per address).
	// Turn it on only behind a reverse proxy that sets the header itself, such as Coolify's Traefik.
	TrustProxyHeaders bool
	// AccountStorageBytes is the room one account's recordings may take; beyond it the oldest are deleted.
	AccountStorageBytes int64

	// SpeechURL is the address of the speech recognition service (scheme://host[:port]). Empty turns recognition
	// off: nothing is queued and no worker runs.
	SpeechURL string
	// SpeechToken is the secret shared with that service; empty only for a service started without one.
	SpeechToken string
	// SpeechTimeout limits one call to the service, from sending the recording to getting the text.
	SpeechTimeout time.Duration
	// SpeechMaxAttempts is how many attempts that count a recording gets before it is given up on.
	SpeechMaxAttempts int
	// SpeechMaxAudio is the longest recording sent to the service. The model's memory was measured to 60 seconds.
	SpeechMaxAudio time.Duration

	// TelegramToken is the bot's token from @BotFather. Empty turns the Telegram bot off: no page section, no
	// links to chats, no sending. It is a secret: it is never printed, not even in a message about its being wrong.
	TelegramToken string
	// TelegramAPIURL is the address of the Bot API (scheme://host[:port]); empty means Telegram's own. For tests
	// and for a Bot API server of one's own.
	TelegramAPIURL string
	// TelegramWait is how long a recording waits for its text before it is sent without it.
	TelegramWait time.Duration
	// TelegramTimeout limits one call to the Bot API (a long poll adds its own wait).
	TelegramTimeout time.Duration
}

// telegramToken is the shape of a bot token: the bot's number, a colon, then the secret part.
var telegramToken = regexp.MustCompile(`^[0-9]{3,}:[A-Za-z0-9_-]{20,}$`)

// DefaultAccountStorageBytes is 100 MiB. MinAccountStorageBytes only guards against a typo (a limit in megabytes
// or kilobytes by mistake); it is lower than one large recording on purpose, so that the storage warning can be
// tried with a few recordings. A recording that alone exceeds the limit is still accepted and keeps the
// account's older recordings out, never itself.
const (
	DefaultAccountStorageBytes = 100 << 20
	MinAccountStorageBytes     = 100_000
)

// Load reads the configuration through getenv (os.Getenv in production). It reports
// every problem at once rather than stopping at the first.
func Load(getenv func(string) string) (Config, error) {
	var problems []error
	bad := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	c := Config{
		DatabaseURL: strings.TrimSpace(getenv("DATABASE_URL")),
		StorageDir:  orDefault(getenv("STORAGE_DIR"), "./data"),
		Port:        3000,
		MaxDBConns:  10,

		CookieSecure:        true,
		AccountStorageBytes: DefaultAccountStorageBytes,

		SpeechTimeout:     90 * time.Second,
		SpeechMaxAttempts: 5,
		SpeechMaxAudio:    65 * time.Second,

		TelegramWait:    3 * time.Minute,
		TelegramTimeout: 30 * time.Second,
	}

	if c.DatabaseURL == "" {
		bad("DATABASE_URL is required")
	}

	if v := strings.TrimSpace(getenv("PORT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			bad("PORT must be a number from 1 to 65535, got %q", v)
		} else {
			c.Port = n
		}
	}

	c.DefaultServerURL = strings.TrimRight(orDefault(getenv("DEFAULT_SERVER_URL"), "http://localhost:"+strconv.Itoa(c.Port)), "/")
	if u, err := url.Parse(c.DefaultServerURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		bad("DEFAULT_SERVER_URL must be an http(s) URL, got %q", c.DefaultServerURL)
	}

	if v := strings.TrimSpace(getenv("MIGRATE_ON_START")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			bad("MIGRATE_ON_START must be true or false, got %q", v)
		}
		c.MigrateOnStart = b
	}

	if v := strings.TrimSpace(getenv("LOG_LEVEL")); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			bad("LOG_LEVEL must be debug, info, warn or error, got %q", v)
		}
	}

	if v := strings.TrimSpace(getenv("MAX_DB_CONNS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			bad("MAX_DB_CONNS must be a number from 1 to 100, got %q", v)
		} else {
			c.MaxDBConns = int32(n)
		}
	}

	if v := strings.TrimSpace(getenv("COOKIE_SECURE")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			bad("COOKIE_SECURE must be true or false, got %q", v)
		} else {
			c.CookieSecure = b
		}
	}

	if v := strings.TrimSpace(getenv("TRUST_PROXY_HEADERS")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			bad("TRUST_PROXY_HEADERS must be true or false, got %q", v)
		} else {
			c.TrustProxyHeaders = b
		}
	}

	if v := strings.TrimSpace(getenv("STORAGE_ACCOUNT_BYTES")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < MinAccountStorageBytes {
			bad("STORAGE_ACCOUNT_BYTES must be a number of bytes, at least %d, got %q", MinAccountStorageBytes, v)
		} else {
			c.AccountStorageBytes = n
		}
	}

	if v := strings.TrimSpace(getenv("STT_URL")); v != "" {
		if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("STT_URL must be an http(s) URL (scheme://host[:port]), got %q", v)
		} else {
			c.SpeechURL = strings.TrimRight(v, "/")
		}
	}
	c.SpeechToken = strings.TrimSpace(getenv("STT_TOKEN"))
	if c.SpeechURL == "" && c.SpeechToken != "" {
		bad("STT_TOKEN is set but STT_URL is not: speech recognition would stay off")
	}

	if v := strings.TrimSpace(getenv("STT_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > 10*time.Minute {
			bad("STT_TIMEOUT must be a duration from 1s to 10m (for example 90s), got %q", v)
		} else {
			c.SpeechTimeout = d
		}
	}

	if v := strings.TrimSpace(getenv("STT_MAX_ATTEMPTS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 20 {
			bad("STT_MAX_ATTEMPTS must be a number from 1 to 20, got %q", v)
		} else {
			c.SpeechMaxAttempts = n
		}
	}

	if v := strings.TrimSpace(getenv("STT_MAX_AUDIO_SECONDS")); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 1 || n > 600 {
			bad("STT_MAX_AUDIO_SECONDS must be a number of seconds from 1 to 600, got %q", v)
		} else {
			c.SpeechMaxAudio = time.Duration(n * float64(time.Second))
		}
	}

	if v := strings.TrimSpace(getenv("TELEGRAM_BOT_TOKEN")); v != "" {
		if !telegramToken.MatchString(v) {
			bad("TELEGRAM_BOT_TOKEN does not look like a token from @BotFather (digits, a colon, then letters and digits)") // not echoed: it is a secret
		} else {
			c.TelegramToken = v
		}
	}
	if v := strings.TrimSpace(getenv("TELEGRAM_API_URL")); v != "" {
		if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			bad("TELEGRAM_API_URL must be scheme://host[:port], got %q", v)
		} else if c.TelegramToken == "" && strings.TrimSpace(getenv("TELEGRAM_BOT_TOKEN")) == "" {
			bad("TELEGRAM_API_URL is set but TELEGRAM_BOT_TOKEN is not: the Telegram bot would stay off")
		} else {
			c.TelegramAPIURL = strings.TrimRight(v, "/")
		}
	}
	if v := strings.TrimSpace(getenv("TELEGRAM_WAIT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > 30*time.Minute {
			bad("TELEGRAM_WAIT must be a duration from 1s to 30m (for example 3m), got %q", v)
		} else {
			c.TelegramWait = d
		}
	}
	if v := strings.TrimSpace(getenv("TELEGRAM_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > 5*time.Minute {
			bad("TELEGRAM_TIMEOUT must be a duration from 1s to 5m (for example 30s), got %q", v)
		} else {
			c.TelegramTimeout = d
		}
	}

	// ALLOWED_ORIGINS: see Config.AllowedOrigins - the CSRF check's list, not CORS.
	for _, origin := range strings.Split(getenv("ALLOWED_ORIGINS"), ",") {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin == "" {
			continue
		}
		if u, err := url.Parse(origin); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
			bad("ALLOWED_ORIGINS must list origins like https://app.example.com, got %q", origin)
			continue
		}
		c.AllowedOrigins = append(c.AllowedOrigins, origin)
	}

	if len(problems) > 0 {
		return Config{}, errors.Join(problems...)
	}
	return c, nil
}

func orDefault(value, fallback string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}
	return fallback
}
