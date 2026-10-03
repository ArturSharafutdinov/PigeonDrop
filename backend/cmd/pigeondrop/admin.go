package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"golang.org/x/term"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/config"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// The operator's commands. They talk to the database directly, so whoever can run them (inside the
// container, on the server) is by definition allowed to: there is no admin login to protect or to forget.
const adminUsage = `usage:
  pigeondrop invite create [-expires 168h] [-note TEXT]   print a new invitation code (shown only once)
  pigeondrop invite list                                  list invitations and who used them
  pigeondrop invite revoke <id>                           withdraw an unused invitation
  pigeondrop user list                                    list users
  pigeondrop user set-password <email>                    set a new password and end the user's sessions;
                                                          read from the terminal, or from stdin if piped
`

func admin(ctx context.Context, cfg config.Config, group string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return fmt.Errorf("usage: pigeondrop %s <command>", group)
	}

	pool, err := db.Connect(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	if pending, err := db.Pending(ctx, pool); err != nil {
		return err
	} else if pending > 0 {
		return fmt.Errorf("%d database migration(s) pending: run `pigeondrop migrate up` first", pending)
	}
	q := dbgen.New(pool)

	switch group + " " + args[0] {
	case "invite create":
		return inviteCreate(ctx, q, args[1:], stdout, stderr)
	case "invite list":
		return inviteList(ctx, q, stdout)
	case "invite revoke":
		return inviteRevoke(ctx, q, args[1:], stdout)
	case "user list":
		return userList(ctx, q, stdout)
	case "user set-password":
		return userSetPassword(ctx, q, args[1:], stdin, stderr)
	default:
		fmt.Fprint(stderr, adminUsage)
		return fmt.Errorf("unknown command %q", group+" "+args[0])
	}
}

func inviteCreate(ctx context.Context, q dbgen.Querier, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("invite create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	expires := flags.Duration("expires", 7*24*time.Hour, "how long the code stays valid")
	note := flags.String("note", "", "a reminder of who the code is for")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *expires <= 0 {
		return errors.New("-expires must be positive")
	}

	code, hash, err := auth.NewInviteCode()
	if err != nil {
		return err
	}
	now := time.Now()
	if _, err := q.CreateInvitation(ctx, dbgen.CreateInvitationParams{
		CodeHash: hash, Note: strings.TrimSpace(*note), CreatedAt: now, ExpiresAt: now.Add(*expires),
	}); err != nil {
		return fmt.Errorf("store invitation: %w", err)
	}
	// Only the code goes to stdout, so `CODE=$(pigeondrop invite create)` works; the rest is for the person.
	fmt.Fprintln(stdout, code)
	fmt.Fprintf(stderr, "Valid until %s. Only a hash is stored: this is the only time the code is shown.\n", now.Add(*expires).UTC().Format("2006-01-02 15:04 UTC"))
	return nil
}

func inviteList(ctx context.Context, q dbgen.Querier, stdout io.Writer) error {
	rows, err := q.ListInvitations(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tEXPIRES\tNOTE")
	for _, r := range rows {
		state := "unused"
		switch {
		case r.UsedAt != nil && r.UsedByEmail != nil:
			state = "used by " + *r.UsedByEmail
		case r.UsedAt != nil:
			state = "used (account deleted)"
		case !r.ExpiresAt.After(now):
			state = "expired"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, state, r.ExpiresAt.UTC().Format("2006-01-02 15:04"), r.Note)
	}
	return w.Flush()
}

func inviteRevoke(ctx context.Context, q dbgen.Querier, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: pigeondrop invite revoke <id>")
	}
	id, err := uuid.Parse(args[0])
	if err != nil {
		return fmt.Errorf("%q is not an invitation id (see `pigeondrop invite list`)", args[0])
	}
	n, err := q.RevokeInvitation(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("no unused invitation with that id (a used one cannot be withdrawn)")
	}
	fmt.Fprintln(stdout, "revoked")
	return nil
}

func userList(ctx context.Context, q dbgen.Querier, stdout io.Writer) error {
	rows, err := q.ListUsers(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tEMAIL\tPLAN\tLANGUAGE\tCREATED")
	for _, u := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", u.ID, u.Email, u.Plan, u.Language, u.CreatedAt.UTC().Format("2006-01-02 15:04"))
	}
	return w.Flush()
}

// userSetPassword is the way back in for someone who forgot their password: the MVP sends no mail, so the
// operator sets a new one and passes it on. All the user's sessions end.
func userSetPassword(ctx context.Context, q dbgen.Querier, args []string, stdin io.Reader, stderr io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: pigeondrop user set-password <email>")
	}
	email, err := auth.NormalizeEmail(args[0])
	if err != nil {
		return fmt.Errorf("%q is not an email address", args[0])
	}
	user, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("no user with the email %s", email)
	}

	password, err := readNewPassword(stdin, stderr)
	if err != nil {
		return err
	}
	if err := auth.ValidatePassword(password); err != nil {
		return err
	}
	hash, err := auth.NewHasher(auth.DefaultParams, 1).Hash(ctx, password)
	if err != nil {
		return err
	}
	if err := q.SetUserPassword(ctx, dbgen.SetUserPasswordParams{ID: user.ID, PasswordHash: hash}); err != nil {
		return err
	}
	if err := q.DeleteUserSessions(ctx, user.ID); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Password of %s changed; every session of that user has ended.\n", email)
	return nil
}

// readNewPassword asks twice, without echo, on a terminal; otherwise (a pipe) it reads one line. The
// password is never taken from the command line, where it would land in the shell history.
func readNewPassword(stdin io.Reader, stderr io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stderr, "New password: ")
		first, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(stderr, "Repeat it: ")
		second, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			return "", errors.New("the two entries differ")
		}
		return string(first), nil
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", errors.New("no password on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
