// Package testdb gives tests a private, freshly migrated PostgreSQL database.
//
// It connects to the server named by TEST_DATABASE_URL (default: the one that
// scripts/dev-db.sh starts), creates a database with a random name, applies the
// migrations, and drops it again when the test ends - so tests never see each
// other's data and never touch any existing database.
//
// With the database unreachable a test is skipped, unless REQUIRE_DB=1 is set (CI
// sets it), in which case it fails: a green run must not silently skip every test.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db"
)

const defaultURL = "postgres://postgres@127.0.0.1:55432/postgres?sslmode=disable"

// URL returns the connection URL of the test server's maintenance database.
func URL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return defaultURL
}

func unavailable(t testing.TB, err error) {
	t.Helper()
	if os.Getenv("REQUIRE_DB") == "1" {
		t.Fatalf("test database unavailable (REQUIRE_DB=1): %v", err)
	}
	t.Skipf("test database unavailable (start one with scripts/dev-db.sh up, or set TEST_DATABASE_URL): %v", err)
}

// New returns a pool connected to a new, migrated database that is dropped on cleanup.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	_, pool := create(t)
	return pool
}

// NewURL is like New but also returns the database's connection URL, for tests that
// start the whole program and hand it a DATABASE_URL.
func NewURL(t testing.TB) (string, *pgxpool.Pool) {
	t.Helper()
	return create(t)
}

func create(t testing.TB) (string, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, URL())
	if err != nil {
		unavailable(t, err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		unavailable(t, err)
	}

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "pigeondrop_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}

	u, err := url.Parse(URL())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name

	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if _, err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
		admin.Close()
	})
	return u.String(), pool
}
