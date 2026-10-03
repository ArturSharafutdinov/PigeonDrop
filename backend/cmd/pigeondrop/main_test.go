package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/testdb"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func envOf(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestVersionAndHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"version"}, envOf(nil), &out, &errOut); err != nil || strings.TrimSpace(out.String()) != version {
		t.Fatalf("version: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := run(context.Background(), []string{"help"}, envOf(nil), &out, &errOut); err != nil || !strings.Contains(out.String(), "serve") {
		t.Fatalf("help: %q, %v", out.String(), err)
	}
}

func TestUnknownAndMissingCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run(context.Background(), nil, envOf(nil), &out, &errOut); err == nil {
		t.Error("no command must fail")
	}
	if err := run(context.Background(), []string{"frobnicate"}, envOf(nil), &out, &errOut); err == nil {
		t.Error("unknown command must fail")
	}
}

func TestServeNeedsConfiguration(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run(context.Background(), []string{"serve"}, envOf(nil), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("got %v", err)
	}
}

// Starts the real program against a real database, checks it answers, and stops it the
// way Docker does (a cancelled context stands in for SIGTERM).
func TestServeMigratesAnswersAndShutsDownCleanly(t *testing.T) {
	url, _ := testdb.NewURL(t)
	port := freePort(t)
	env := envOf(map[string]string{
		"DATABASE_URL": url, "PORT": fmt.Sprint(port), "STORAGE_DIR": t.TempDir(), "MIGRATE_ON_START": "true", "LOG_LEVEL": "error",
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var out, errOut bytes.Buffer
	go func() { done <- run(ctx, []string{"serve"}, env, &out, &errOut) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := healthcheck(context.Background(), env); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server never became healthy; log: %s", errOut.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %v %v", res, err)
	}
	res.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned an error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop after cancellation")
	}
}

func TestServeRefusesWhenMigrationsArePending(t *testing.T) {
	// a database that exists but was never migrated
	url, pool := testdb.NewURL(t)
	if _, err := pool.Exec(context.Background(), `DROP TABLE processed_uploads; DROP TABLE accounts; DROP TABLE goose_db_version`); err != nil {
		t.Fatal(err)
	}
	env := envOf(map[string]string{"DATABASE_URL": url, "PORT": fmt.Sprint(freePort(t)), "STORAGE_DIR": t.TempDir(), "LOG_LEVEL": "error"})

	// The deadline turns "serve wrongly started and never returns" into a failure instead of a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	err := run(ctx, []string{"serve"}, env, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "migration(s) pending") {
		t.Fatalf("got %v", err)
	}
}

func TestMigrateUpAndStatus(t *testing.T) {
	url, pool := testdb.NewURL(t)
	if _, err := pool.Exec(context.Background(), `DROP TABLE processed_uploads; DROP TABLE accounts; DROP TABLE goose_db_version`); err != nil {
		t.Fatal(err)
	}
	env := envOf(map[string]string{"DATABASE_URL": url})

	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"migrate", "status"}, env, &out, &errOut); err != nil || !strings.Contains(out.String(), "pending") {
		t.Fatalf("status before: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := run(context.Background(), []string{"migrate", "up"}, env, &out, &errOut); err != nil || !strings.Contains(out.String(), "applied 1 migration") {
		t.Fatalf("up: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := run(context.Background(), []string{"migrate", "status"}, env, &out, &errOut); err != nil || !strings.Contains(out.String(), "applied") {
		t.Fatalf("status after: %q, %v", out.String(), err)
	}
	if err := run(context.Background(), []string{"migrate", "sideways"}, env, &out, &errOut); err == nil {
		t.Error("an unknown migrate subcommand must fail")
	}
}
