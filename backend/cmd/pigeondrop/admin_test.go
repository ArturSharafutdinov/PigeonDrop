package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/httpapi"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/testdb"
)

type cli struct {
	t    *testing.T
	env  func(string) string
	url  string
	pool *pgxpool.Pool
	q    *dbgen.Queries
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	url, pool := testdb.NewURL(t)
	return &cli{t: t, env: envOf(map[string]string{"DATABASE_URL": url}), url: url, pool: pool, q: dbgen.New(pool)}
}

// run executes one command the way main does and returns what it printed.
func (c *cli) run(stdin string, args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	err = run(context.Background(), args, c.env, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), err
}

func (c *cli) mustRun(stdin string, args ...string) (stdout, stderr string) {
	c.t.Helper()
	out, errOut, err := c.run(stdin, args...)
	if err != nil {
		c.t.Fatalf("pigeondrop %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, errOut)
	}
	return out, errOut
}

var inviteCodeShape = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){4}$`)

func TestInviteCreatePrintsOnlyTheCodeOnStdoutAndStoresOnlyItsHash(t *testing.T) {
	c := newCLI(t)
	stdout, stderr := c.mustRun("", "invite", "create", "-note", "  for Alice  ", "-expires", "48h")

	code := strings.TrimSpace(stdout)
	if !inviteCodeShape.MatchString(code) || stdout != code+"\n" {
		t.Fatalf("stdout is %q, want just a code", stdout)
	}
	if !strings.Contains(stderr, "Valid until") || strings.Contains(stderr, code) {
		t.Fatalf("stderr: %q", stderr)
	}

	hash, _ := auth.ParseInviteCode(code)
	if _, err := c.q.FindUsableInvitation(context.Background(), dbgen.FindUsableInvitationParams{CodeHash: hash, Now: time.Now()}); err != nil {
		t.Fatalf("the code printed is not the one stored: %v", err)
	}
	var dump string
	if err := c.pool.QueryRow(context.Background(), `SELECT i::text FROM invitations i`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(dump), strings.ReplaceAll(code, "-", "")) || strings.Contains(strings.ToUpper(dump), code) {
		t.Fatalf("the code itself is in the database: %s", dump)
	}

	invites, _ := c.q.ListInvitations(context.Background())
	if len(invites) != 1 || invites[0].Note != "for Alice" {
		t.Fatalf("stored: %+v", invites)
	}
	if left := time.Until(invites[0].ExpiresAt); left < 47*time.Hour || left > 48*time.Hour {
		t.Fatalf("expires in %v, want 48h", left)
	}
}

func TestInviteCreateDefaultsToAWeek(t *testing.T) {
	c := newCLI(t)
	c.mustRun("", "invite", "create")
	invites, _ := c.q.ListInvitations(context.Background())
	if left := time.Until(invites[0].ExpiresAt); left < 167*time.Hour || left > 168*time.Hour {
		t.Fatalf("expires in %v, want 168h", left)
	}
}

func TestInviteCreateRefusesBadArguments(t *testing.T) {
	c := newCLI(t)
	for _, args := range [][]string{
		{"invite", "create", "-expires", "0s"},
		{"invite", "create", "-expires", "-5h"},
		{"invite", "create", "-expires", "tomorrow"},
		{"invite", "create", "extra"},
		{"invite", "create", "-nonsense"},
	} {
		if out, _, err := c.run("", args...); err == nil {
			t.Errorf("%v succeeded and printed %q", args, out)
		}
	}
	if invites, _ := c.q.ListInvitations(context.Background()); len(invites) != 0 {
		t.Fatalf("refused commands stored %d invitations", len(invites))
	}
}

func TestInviteListShowsStates(t *testing.T) {
	c := newCLI(t)
	ctx := context.Background()
	c.mustRun("", "invite", "create", "-note", "fresh one")

	past := time.Now().Add(-time.Hour)
	_, expiredHash, _ := auth.NewInviteCode()
	c.q.CreateInvitation(ctx, dbgen.CreateInvitationParams{CodeHash: expiredHash, Note: "old one", CreatedAt: past.Add(-time.Hour), ExpiresAt: past})

	_, usedHash, _ := auth.NewInviteCode()
	c.q.CreateInvitation(ctx, dbgen.CreateInvitationParams{CodeHash: usedHash, Note: "taken one", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	user, err := c.q.CreateUser(ctx, dbgen.CreateUserParams{Email: "bob@example.com", PasswordHash: "x", Language: "en", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	c.q.ConsumeInvitation(ctx, dbgen.ConsumeInvitationParams{CodeHash: usedHash, Now: time.Now(), UsedBy: &user.ID})

	stdout, _ := c.mustRun("", "invite", "list")
	for _, want := range []string{"fresh one", "unused", "old one", "expired", "taken one", "used by bob@example.com"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the list lacks %q:\n%s", want, stdout)
		}
	}
}

func TestInviteRevoke(t *testing.T) {
	c := newCLI(t)
	ctx := context.Background()
	code, _ := c.mustRun("", "invite", "create")
	invites, _ := c.q.ListInvitations(ctx)

	if _, _, err := c.run("", "invite", "revoke", "not-a-uuid"); err == nil {
		t.Error("a malformed id was accepted")
	}
	if _, _, err := c.run("", "invite", "revoke", "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Error("an unknown id was accepted")
	}
	if _, _, err := c.run("", "invite", "revoke"); err == nil {
		t.Error("revoke without an id was accepted")
	}
	if left, _ := c.q.ListInvitations(ctx); len(left) != 1 {
		t.Fatal("a refused revoke removed something")
	}

	out, _ := c.mustRun("", "invite", "revoke", invites[0].ID.String())
	if !strings.Contains(out, "revoked") {
		t.Errorf("stdout: %q", out)
	}
	hash, _ := auth.ParseInviteCode(strings.TrimSpace(code))
	if _, err := c.q.FindUsableInvitation(ctx, dbgen.FindUsableInvitationParams{CodeHash: hash, Now: time.Now()}); err == nil {
		t.Fatal("a revoked code still works")
	}

	// A used invitation stays as the record of who came through it.
	_, usedHash, _ := auth.NewInviteCode()
	row, _ := c.q.CreateInvitation(ctx, dbgen.CreateInvitationParams{CodeHash: usedHash, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	user, _ := c.q.CreateUser(ctx, dbgen.CreateUserParams{Email: "bob@example.com", PasswordHash: "x", Language: "en", CreatedAt: time.Now()})
	c.q.ConsumeInvitation(ctx, dbgen.ConsumeInvitationParams{CodeHash: usedHash, Now: time.Now(), UsedBy: &user.ID})
	if _, _, err := c.run("", "invite", "revoke", row.ID.String()); err == nil {
		t.Fatal("a used invitation was withdrawn")
	}
}

func TestUserListAndSetPassword(t *testing.T) {
	c := newCLI(t)
	ctx := context.Background()
	hasher := auth.NewHasher(auth.DefaultParams, 1)
	oldHash, _ := hasher.Hash(ctx, "the old password")
	user, err := c.q.CreateUser(ctx, dbgen.CreateUserParams{Email: "alice@example.com", PasswordHash: oldHash, Language: "ru", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, hash, _ := auth.NewSessionToken()
		c.q.CreateSession(ctx, dbgen.CreateSessionParams{UserID: user.ID, TokenHash: hash, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(time.Hour)})
	}

	list, _ := c.mustRun("", "user", "list")
	if !strings.Contains(list, "alice@example.com") || !strings.Contains(list, "ru") || strings.Contains(list, "argon2") || strings.Contains(list, oldHash) {
		t.Fatalf("user list:\n%s", list)
	}

	const next = "the brand new password"
	stdout, stderr := c.mustRun(next+"\n", "user", "set-password", "  ALICE@example.com ")
	if strings.Contains(stdout+stderr, next) {
		t.Fatal("the password was echoed")
	}
	stored, _ := c.q.GetUserByEmail(ctx, "alice@example.com")
	if ok, _, _ := hasher.Verify(ctx, stored.PasswordHash, next); !ok {
		t.Fatal("the new password does not work")
	}
	if ok, _, _ := hasher.Verify(ctx, stored.PasswordHash, "the old password"); ok {
		t.Fatal("the old password still works")
	}
	var sessions int
	c.pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessions)
	if sessions != 0 {
		t.Fatalf("%d sessions survived the password change", sessions)
	}
}

func TestSetPasswordRefusesWhatItShould(t *testing.T) {
	c := newCLI(t)
	ctx := context.Background()
	hash, _ := auth.NewHasher(auth.DefaultParams, 1).Hash(ctx, "the old password")
	c.q.CreateUser(ctx, dbgen.CreateUserParams{Email: "alice@example.com", PasswordHash: hash, Language: "en", CreatedAt: time.Now()})

	for name, tc := range map[string]struct {
		stdin string
		args  []string
	}{
		"unknown user":       {"a long enough password\n", []string{"user", "set-password", "nobody@example.com"}},
		"not an email":       {"a long enough password\n", []string{"user", "set-password", "alice"}},
		"no argument":        {"a long enough password\n", []string{"user", "set-password"}},
		"too short":          {"short\n", []string{"user", "set-password", "alice@example.com"}},
		"nothing on stdin":   {"", []string{"user", "set-password", "alice@example.com"}},
		"empty line":         {"\n", []string{"user", "set-password", "alice@example.com"}},
		"password as an arg": {"", []string{"user", "set-password", "alice@example.com", "a long enough password"}},
	} {
		if _, _, err := c.run(tc.stdin, tc.args...); err == nil {
			t.Errorf("%s: succeeded", name)
		}
	}
	stored, _ := c.q.GetUserByEmail(ctx, "alice@example.com")
	if stored.PasswordHash != hash {
		t.Fatal("a refused command changed the password")
	}

	// A password typed without a final newline (a here-string, printf) is still read.
	c.mustRun("a long enough password", "user", "set-password", "alice@example.com")
	stored, _ = c.q.GetUserByEmail(ctx, "alice@example.com")
	if stored.PasswordHash == hash {
		t.Fatal("the password was not changed")
	}
}

func TestAdminCommandsNeedTheMigrationsAndASubcommand(t *testing.T) {
	url, pool := testdb.NewURL(t)
	pool.Exec(context.Background(), unmigrate)
	c := &cli{t: t, env: envOf(map[string]string{"DATABASE_URL": url})}
	if _, _, err := c.run("", "invite", "create"); err == nil || !strings.Contains(err.Error(), "migration(s) pending") {
		t.Fatalf("got %v", err)
	}

	c = newCLI(t)
	for _, args := range [][]string{{"invite"}, {"invite", "frobnicate"}, {"user"}, {"user", "delete"}} {
		if _, stderr, err := c.run("", args...); err == nil || !strings.Contains(stderr, "usage:") {
			t.Errorf("%v: err=%v stderr=%q", args, err, stderr)
		}
	}
	if _, _, err := newCLIWithoutDB(t).run("", "invite", "create"); err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("got %v", err)
	}
}

func newCLIWithoutDB(t *testing.T) *cli { return &cli{t: t, env: envOf(nil)} }

// The code the operator prints must work in the HTTP API: the two agree on how a code is hashed.
func TestACodeFromTheCommandRegistersAUser(t *testing.T) {
	c := newCLI(t)
	stdout, _ := c.mustRun("", "invite", "create")
	code := strings.TrimSpace(stdout)

	handler := httpapi.NewHandler(&httpapi.Server{
		Pinger: c.pool, DB: c.q, Tx: db.InTx(c.pool), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hasher: auth.NewHasher(auth.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1}, 2), CookieSecure: true,
	})
	body, _ := json.Marshal(map[string]string{"inviteCode": code, "email": "alice@example.com", "password": "a long enough password"})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registering with the printed code: %d %s", rec.Code, rec.Body.String())
	}

	list, _ := c.mustRun("", "invite", "list")
	if !strings.Contains(list, "used by alice@example.com") {
		t.Fatalf("the list does not show the registration:\n%s", list)
	}
	users, _ := c.mustRun("", "user", "list")
	if !strings.Contains(users, "alice@example.com") {
		t.Fatalf("the user list lacks the new user:\n%s", users)
	}
}
