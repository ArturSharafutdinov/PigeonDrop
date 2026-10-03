package db_test

import (
	"context"
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

	for _, table := range []string{"accounts", "processed_uploads"} {
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
