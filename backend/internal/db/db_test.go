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

	for _, table := range []string{"accounts", "processed_uploads", "users", "invitations", "sessions", "devices", "device_pairings"} {
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

func TestDeviceSchemaConstraints(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	exec := func(query string, args ...any) error {
		_, err := pool.Exec(ctx, query, args...)
		return err
	}
	if err := exec(`INSERT INTO users (email, password_hash) VALUES ('alice@example.com', 'x')`); err != nil {
		t.Fatal(err)
	}
	var user string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM users`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO devices (user_id, name, token_hash) VALUES ($1, 'watch', '\x01')`, user); err != nil {
		t.Fatal(err)
	}
	pairing := func(extra string) string {
		return `INSERT INTO device_pairings (device_code_hash, user_code, expires_at` + extra + `) VALUES `
	}
	if err := exec(pairing("") + `('\x10', 'BCDFGHJK', now())`); err != nil {
		t.Fatal(err)
	}

	for name, query := range map[string]string{
		"the same token twice":         `INSERT INTO devices (user_id, name, token_hash) VALUES ('` + user + `', 'again', '\x01')`,
		"an empty device name":         `INSERT INTO devices (user_id, name, token_hash) VALUES ('` + user + `', '', '\x02')`,
		"a device name over 64":        `INSERT INTO devices (user_id, name, token_hash) VALUES ('` + user + `', '` + strings.Repeat("x", 65) + `', '\x03')`,
		"a device of nobody":           `INSERT INTO devices (user_id, name, token_hash) VALUES (gen_random_uuid(), 'x', '\x04')`,
		"the same user code twice":     pairing("") + `('\x11', 'BCDFGHJK', now())`,
		"the same device code twice":   pairing("") + `('\x10', 'BBBBBBBB', now())`,
		"a user code with a vowel":     pairing("") + `('\x12', 'ABCDFGHJ', now())`,
		"a user code too short":        pairing("") + `('\x13', 'BCDFGHJ', now())`,
		"a lower-case user code":       pairing("") + `('\x14', 'bcdfghjk', now())`,
		"a client name over 64":        pairing(", client_name") + `('\x15', 'CCCCCCCC', now(), '` + strings.Repeat("x", 65) + `')`,
		"approved without a user":      pairing(", approved_at, device_name") + `('\x16', 'DDDDDDDD', now(), now(), 'w')`,
		"approved without a name":      pairing(", approved_at, user_id") + `('\x17', 'FFFFFFFF', now(), now(), '` + user + `')`,
		"a user without an approval":   pairing(", user_id, device_name") + `('\x18', 'GGGGGGGG', now(), '` + user + `', 'w')`,
		"redeemed without an approval": pairing(", redeemed_at") + `('\x19', 'HHHHHHHH', now(), now())`,
	} {
		if err := exec(query); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// A complete approval and its redemption are fine, and both go with the user.
	if err := exec(pairing(", user_id, device_name, approved_at, redeemed_at") + `('\x20', 'JJJJJJJJ', now(), '` + user + `', 'w', now(), now())`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`DELETE FROM users WHERE id = $1`, user); err != nil {
		t.Fatal(err)
	}
	var devices, approved int
	pool.QueryRow(ctx, `SELECT count(*) FROM devices`).Scan(&devices)
	pool.QueryRow(ctx, `SELECT count(*) FROM device_pairings WHERE approved_at IS NOT NULL`).Scan(&approved)
	if devices != 0 || approved != 0 {
		t.Fatalf("after deleting the user: %d devices and %d approved pairings left", devices, approved)
	}
}
