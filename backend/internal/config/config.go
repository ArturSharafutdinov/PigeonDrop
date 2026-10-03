// Package config reads the server's settings from environment variables, the way
// Coolify and Docker pass them in. Nothing is read from files.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
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
	// AllowedOrigins are web pages, besides this server's own address, whose scripts may call the API with a
	// signed-in session - for example the front end's dev server. Each is scheme://host[:port].
	AllowedOrigins []string
	// TrustProxyHeaders believes X-Forwarded-For for the client's address (the sign-in limits are per address).
	// Turn it on only behind a reverse proxy that sets the header itself, such as Coolify's Traefik.
	TrustProxyHeaders bool
}

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

		CookieSecure: true,
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
