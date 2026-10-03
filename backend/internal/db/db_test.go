package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/testdb"
)

func TestMigrationsApplyAndAreIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t) // already migrated once by the helper

	pending, err := db.Pending(ctx, pool)
	if err != nil || pending != 0 {
		t.Fatalf("pending after migrate = %d, %v", pending, err)
	}
	applied, err := db.MigrateUp(ctx, pool)
	if err != nil || applied != 0 {
		t.Fatalf("second MigrateUp applied %d, %v", applied, err)
	}

	for _, table := range []string{"accounts", "processed_uploads", "users", "invitations", "sessions"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil || !exists {
			t.Errorf("table %s missing (%v)", table, err)
		}
	}
}

func TestMigrationStatusListsEveryMigration(t *testing.T) {
	statuses, err := db.Status(context.Background(), testdb.New(t))
	if err != nil || len(statuses) == 0 {
		t.Fatalf("status: %d, %v", len(statuses), err)
	}
	for _, s := range statuses {
		if s.State != "applied" {
			t.Errorf("migration %d is %q, want applied", s.Source.Version, s.State)
		}
	}
}

func TestConnectRejectsBadURLAndUnreachableServer(t *testing.T) {
	ctx := context.Background()
	if _, err := db.Connect(ctx, "not a url", 2); err == nil {
		t.Error("a malformed URL must fail")
	}
	if _, err := db.Connect(ctx, "postgres://postgres@127.0.0.1:1/x?sslmode=disable&connect_timeout=1", 2); err == nil {
		t.Error("an unreachable server must fail")
	}
}

// The schema refuses what the application is meant never to store, whatever code writes to it.
func TestUserSchemaConstraints(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	exec := func(query string, args ...any) error {
		_, err := pool.Exec(ctx, query, args...)
		return err
	}

	if err := exec(`INSERT INTO users (email, password_hash) VALUES ('alice@example.com', 'x')`); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM users`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	for name, query := range map[string]string{
		"email in upper case":         `INSERT INTO users (email, password_hash) VALUES ('Bob@example.com', 'x')`,
		"the same email twice":        `INSERT INTO users (email, password_hash) VALUES ('alice@example.com', 'x')`,
		"email over 254":              `INSERT INTO users (email, password_hash) VALUES ('` + strings.Repeat("a", 250) + `@b.co', 'x')`,
		"no password hash":            `INSERT INTO users (email) VALUES ('bob@example.com')`,
		"unknown plan":                `INSERT INTO users (email, password_hash, plan) VALUES ('bob@example.com', 'x', 'gold')`,
		"unknown language":            `INSERT INTO users (email, password_hash, language) VALUES ('bob@example.com', 'x', 'de')`,
		"the same invitation twice":   `INSERT INTO invitations (code_hash, expires_at) VALUES ('\x01', now()), ('\x01', now())`,
		"used by someone, never used": `INSERT INTO invitations (code_hash, expires_at, used_by) VALUES ('\x02', now(), '` + id + `')`,
		"session of nobody":           `INSERT INTO sessions (user_id, token_hash, expires_at, absolute_expires_at) VALUES (gen_random_uuid(), '\x03', now(), now())`,
	} {
		if err := exec(query); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// A session goes with its user; an invitation outlives the account that used it and forgets who it was.
	if err := exec(`INSERT INTO sessions (user_id, token_hash, expires_at, absolute_expires_at) VALUES ($1, '\x04', now(), now())`, id); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO invitations (code_hash, expires_at, used_at, used_by) VALUES ('\x05', now(), now(), $1)`, id); err != nil {
		t.Fatal(err)
	}
	if err := exec(`DELETE FROM users WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	var sessions, orphaned int
	pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessions)
	pool.QueryRow(ctx, `SELECT count(*) FROM invitations WHERE used_at IS NOT NULL AND used_by IS NULL`).Scan(&orphaned)
	if sessions != 0 || orphaned != 1 {
		t.Fatalf("after deleting the user: %d sessions left, %d invitations kept as used", sessions, orphaned)
	}
}
