package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/term"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/audio"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/config"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/telegram"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/transcribe"
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
  pigeondrop stt check                                    speech recognition: ask the service (STT_URL, STT_TOKEN) to recognise a second of silence
  pigeondrop stt status                                   speech recognition: the queue by state, how long the oldest waits, failures
  pigeondrop stt enqueue -missing [-max-audio 65s]        queue the recordings that were never queued (received before recognition was on)
  pigeondrop stt retry -failed                            give every recording that was given up on another try
  pigeondrop telegram check                               Telegram bot: ask Telegram who the bot is (TELEGRAM_BOT_TOKEN) and whether updates can be received
`

func admin(ctx context.Context, cfg config.Config, group string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return fmt.Errorf("usage: pigeondrop %s <command>", group)
	}

	// The commands that do not need the database: they are how to find out that the other side can be reached.
	if group == "stt" && args[0] == "check" {
		return sttCheck(ctx, cfg, args[1:], stdout)
	}
	if group == "telegram" && args[0] == "check" {
		return telegramCheck(ctx, cfg, args[1:], stdout)
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
	case "stt status":
		return sttStatus(ctx, q, cfg, time.Now(), stdout)
	case "stt enqueue":
		return sttEnqueue(ctx, q, cfg, args[1:], time.Now(), stdout, stderr)
	case "stt retry":
		return sttRetry(ctx, q, cfg, args[1:], time.Now(), stdout, stderr)
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

// The speech recognition commands work on the queue in the database; the server's worker (a separate process)
// picks the work up on its own within a couple of seconds, so there is nothing to tell it.

var sttStates = []string{"pending", "running", "done", "failed", "skipped"}

// sttStatus says how the queue stands: the count in each state, how long the oldest recording has been waiting,
// and what failed lately.
func sttStatus(ctx context.Context, q dbgen.Querier, cfg config.Config, now time.Time, stdout io.Writer) error {
	counts, err := q.CountTranscriptions(ctx)
	if err != nil {
		return err
	}
	byState := map[string]int64{}
	for _, c := range counts {
		byState[c.State] = c.Total
	}
	oldest, err := q.OldestWaitingTranscription(ctx)
	waiting := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	failedDay, err := q.CountFailedTranscriptionsSince(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return err
	}

	if cfg.SpeechURL != "" {
		fmt.Fprintln(stdout, "recognition: on in this environment (STT_URL is set)")
	} else {
		fmt.Fprintln(stdout, "recognition: off in this environment (STT_URL is not set); the server may differ")
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STATE\tRECORDINGS")
	for _, state := range sttStates {
		fmt.Fprintf(w, "%s\t%d\n", state, byState[state])
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if !waiting {
		fmt.Fprintln(stdout, "waiting: nothing")
	} else {
		fmt.Fprintf(stdout, "oldest waiting since %s (%s ago)\n", oldest.UTC().Format("2006-01-02 15:04:05 UTC"), now.Sub(oldest).Round(time.Second))
	}
	fmt.Fprintf(stdout, "given up on in the last 24 hours: %d\n", failedDay)
	latest, err := q.LatestFailedTranscription(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return err
	default:
		fmt.Fprintf(stdout, "latest failure: %s, %s, message %s\n", formatTime(latest.FinishedAt), deref(latest.ErrorCode), latest.MessageID)
		if latest.LastError != nil {
			fmt.Fprintf(stdout, "  %s\n", *latest.LastError)
		}
	}
	return nil
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "?"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func deref(s *string) string {
	if s == nil {
		return "?"
	}
	return *s
}

// sttEnqueue queues the recordings that were received before recognition was switched on. It does nothing without
// -missing: queueing a whole archive is not a thing to do by an accident of typing.
func sttEnqueue(ctx context.Context, q dbgen.Querier, cfg config.Config, args []string, now time.Time, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("stt enqueue", flag.ContinueOnError)
	flags.SetOutput(stderr)
	missing := flags.Bool("missing", false, "queue every recording that has no place in the queue yet")
	maxAudio := flags.Duration("max-audio", cfg.SpeechMaxAudio, "recordings longer than this are not sent to the model")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if !*missing {
		return errors.New("usage: pigeondrop stt enqueue -missing")
	}
	if *maxAudio <= 0 {
		return errors.New("-max-audio must be positive")
	}

	states, err := q.EnqueueMissingTranscriptions(ctx, dbgen.EnqueueMissingTranscriptionsParams{
		MaxAudioMs: int32(min(maxAudio.Milliseconds(), math.MaxInt32)), Now: now,
	})
	if err != nil {
		return err
	}
	queued, skipped := 0, 0
	for _, state := range states {
		if state == "skipped" {
			skipped++
		} else {
			queued++
		}
	}
	fmt.Fprintf(stdout, "queued %d recordings (and marked %d as not going to the model: cannot be read, or longer than %s)\n", queued, skipped, *maxAudio)
	warnIfOff(cfg, queued, stderr)
	return nil
}

// sttRetry gives every recording that was given up on another try, behind whatever is queued.
func sttRetry(ctx context.Context, q dbgen.Querier, cfg config.Config, args []string, now time.Time, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("stt retry", flag.ContinueOnError)
	flags.SetOutput(stderr)
	failed := flags.Bool("failed", false, "give every recording that was given up on another try")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if !*failed {
		return errors.New("usage: pigeondrop stt retry -failed")
	}
	n, err := q.RetryFailedTranscriptions(ctx, now)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "queued again: %d recordings\n", n)
	warnIfOff(cfg, int(n), stderr)
	return nil
}

// warnIfOff reminds the operator that queued recordings wait for ever if nothing works the queue.
func warnIfOff(cfg config.Config, queued int, stderr io.Writer) {
	if queued > 0 && cfg.SpeechURL == "" {
		fmt.Fprintln(stderr, "note: STT_URL is not set in this environment. If the server runs without it too, nothing will work the queue.")
	}
}

// sttCheck sends the service one second of silence the way the server's worker would, with the address, token and
// timeout the server is configured with, and says whether and how it answered. Run in the server's container it
// answers the questions that cannot be answered from anywhere else: can the two applications reach each other by that
// name, is the token the same on both sides, has the model finished loading. It needs no database and queues nothing.
func sttCheck(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("unexpected argument %q", args[0])
	}
	if cfg.SpeechURL == "" {
		return errors.New("STT_URL is not set in this environment: there is no service to check")
	}
	service, err := transcribe.NewHTTP(cfg.SpeechURL, cfg.SpeechToken, cfg.SpeechTimeout)
	if err != nil {
		return err
	}

	const second = audio.SampleRate * 2 // bytes of 16-bit mono PCM
	started := time.Now()
	result, err := service.Transcribe(ctx, audio.PCMToWAV(make([]byte, second)))
	took := time.Since(started).Round(time.Millisecond)
	if err != nil {
		kind, isCall := transcribe.KindOf(err)
		switch {
		case isCall && kind == transcribe.Busy:
			// It answered, which is what is being checked.
			fmt.Fprintf(stdout, "ok: the service at %s answered (it is busy with another recording right now)\n", cfg.SpeechURL)
			return nil
		case isCall && kind == transcribe.Unavailable:
			return fmt.Errorf("the service at %s cannot be used: %w\n"+
				"check STT_URL (the name must resolve from this container: both applications on one Docker network), "+
				"that the service has finished loading (about 15 seconds after it starts), and that STT_TOKEN is the one the service runs with", cfg.SpeechURL, err)
		default:
			return fmt.Errorf("the service at %s answered badly: %w", cfg.SpeechURL, err)
		}
	}
	fmt.Fprintf(stdout, "ok: the service at %s answered in %s (engine %s, the model took %d ms for %d ms of audio)\n",
		cfg.SpeechURL, took, result.Engine, result.SttMs, result.AudioMs)
	return nil
}

// telegramCheck asks Telegram who the bot is, the way the server will, with the token and the address it is
// configured with, and whether a webhook is set (while one is, the bot cannot receive messages). Run in the server's
// container it answers what cannot be answered from anywhere else: can this server reach Telegram at all (some
// networks cannot), and is the token the right one. It needs no database and sends nothing to anyone.
func telegramCheck(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("unexpected argument %q", args[0])
	}
	if cfg.TelegramToken == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is not set in this environment: the Telegram bot is off")
	}
	client, err := telegram.NewClient(cfg.TelegramAPIURL, cfg.TelegramToken, cfg.TelegramTimeout)
	if err != nil {
		return err
	}

	bot, err := client.GetMe(ctx)
	if err != nil {
		if kind, isCall := telegram.KindOf(err); isCall && kind == telegram.Unavailable {
			return fmt.Errorf("Telegram cannot be used: %w\n"+
				"check that this server can open the Bot API (api.telegram.org: DNS and outgoing HTTPS are not blocked) and that "+
				"TELEGRAM_BOT_TOKEN is the token @BotFather gave for this bot (a 401 means it is not)", err)
		}
		return fmt.Errorf("Telegram answered badly: %w", err)
	}

	info, err := client.GetWebhookInfo(ctx)
	if err != nil {
		return fmt.Errorf("ask Telegram about the webhook: %w", err)
	}
	if info.URL != "" {
		// Only the host is shown: the rest of a webhook's address is often a secret.
		host := "another address"
		if u, err := url.Parse(info.URL); err == nil && u.Host != "" {
			host = u.Host
		}
		return fmt.Errorf("the bot @%s has a webhook set (to %s): while it is, Telegram does not give the server the bot's messages. "+
			"Remove it (the Bot API method deleteWebhook) or use another bot", bot.Username, host)
	}

	fmt.Fprintf(stdout, "ok: the bot @%s (id %d) answers; no webhook is set, so the server can receive its messages (people open https://t.me/%s)\n",
		bot.Username, bot.ID, bot.Username)
	return nil
}
