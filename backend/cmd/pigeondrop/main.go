// Command pigeondrop is the PigeonDrop server.
//
//	pigeondrop serve          run the HTTP server
//	pigeondrop migrate up     apply pending database migrations
//	pigeondrop migrate status list migrations and whether they are applied
//	pigeondrop invite ...     create, list and revoke invitation codes
//	pigeondrop user ...       list users, set a password
//	pigeondrop stt ...        speech recognition: queue status, queue old recordings, retry failed ones
//	pigeondrop healthcheck    exit 0 if the local server answers /healthz (for Docker)
//	pigeondrop version
//
// All settings come from environment variables; see internal/config.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/config"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/httpapi"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/transcribe"
)

// version is set at build time: go build -ldflags "-X main.version=1.2.3".
var version = "dev"

const usage = `usage: pigeondrop <command>

commands:
  serve             run the HTTP server
  migrate up        apply pending database migrations
  migrate status    list migrations and whether they are applied
  invite create     print a new invitation code        (also: invite list, invite revoke <id>)
  user list         list users                         (also: user set-password <email>)
  stt status        speech recognition queue           (also: stt check, stt enqueue -missing, stt retry -failed)
  healthcheck       exit 0 if the local server answers /healthz
  version           print the version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pigeondrop:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("no command given")
	}

	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	case "healthcheck":
		return healthcheck(ctx, getenv)
	case "serve":
		cfg, err := config.Load(getenv)
		if err != nil {
			return fmt.Errorf("configuration:\n%w", err)
		}
		return serve(ctx, cfg, newLogger(stdout, cfg.LogLevel))
	case "migrate":
		cfg, err := config.Load(getenv)
		if err != nil {
			return fmt.Errorf("configuration:\n%w", err)
		}
		return migrate(ctx, cfg, args[1:], stdout)
	case "invite", "user", "stt":
		cfg, err := config.Load(getenv)
		if err != nil {
			return fmt.Errorf("configuration:\n%w", err)
		}
		return admin(ctx, cfg, args[0], args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.MaxDBConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.MigrateOnStart {
		applied, err := db.MigrateUp(ctx, pool)
		if err != nil {
			return err
		}
		logger.Info("migrations applied", "count", applied)
	}
	pending, err := db.Pending(ctx, pool)
	if err != nil {
		return err
	}
	if pending > 0 {
		return fmt.Errorf("%d database migration(s) pending: run `pigeondrop migrate up`, or set MIGRATE_ON_START=true", pending)
	}

	queries := dbgen.New(pool)
	go purgeExpired(ctx, queries, logger)

	messages, err := storage.NewDisk(filepath.Join(cfg.StorageDir, "messages"))
	if err != nil {
		return err
	}

	intake, wake, stopWorker, err := startSpeechRecognition(ctx, cfg, queries, messages, logger)
	if err != nil {
		return err
	}
	// Stops the worker (it gives back the recording it holds) before the database pool is closed.
	defer stopWorker()

	srv := &http.Server{
		Addr: ":" + strconv.Itoa(cfg.Port),
		Handler: httpapi.NewHandler(&httpapi.Server{
			Pinger: pool, DB: queries, Messages: messages, Logger: logger, DefaultServerURL: cfg.DefaultServerURL,
			Transcription: intake, TranscriptionWake: wake,
			Tx: db.InTx(pool), CookieSecure: cfg.CookieSecure, AllowedOrigins: cfg.AllowedOrigins, TrustProxyHeaders: cfg.TrustProxyHeaders,
			Storage: httpapi.StorageLimits{
				MaxAudioBytes: httpapi.DefaultStorageLimits.MaxAudioBytes, AccountBytes: cfg.AccountStorageBytes, WarnPercent: httpapi.DefaultStorageLimits.WarnPercent,
			},
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	failed := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr, "version", version, "default_server_url", cfg.DefaultServerURL)
		failed <- srv.ListenAndServe()
	}()

	select {
	case err := <-failed:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// A redeploy stops the old container with SIGTERM: stop taking new connections and
	// give requests already in flight (an upload mid-conversion) time to finish.
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}
	return nil
}

// startSpeechRecognition starts the worker that turns recordings into text, if a service for it is configured.
// It returns what the upload needs to queue a recording (nil if recognition is off), the function that wakes the
// worker, and the function that stops it and waits until it has put its recording back in the queue.
func startSpeechRecognition(ctx context.Context, cfg config.Config, q dbgen.Querier, files storage.Store, logger *slog.Logger) (*transcribe.Intake, func(), func(), error) {
	if cfg.SpeechURL == "" {
		return nil, nil, func() {}, nil
	}
	service, err := transcribe.NewHTTP(cfg.SpeechURL, cfg.SpeechToken, cfg.SpeechTimeout)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("speech recognition: %w", err)
	}
	if cfg.SpeechToken == "" {
		logger.Warn("speech recognition is set up without a token: only a service started without one accepts that")
	}
	worker := transcribe.NewWorker(q, files, service, logger, transcribe.Settings{
		MaxAttempts: cfg.SpeechMaxAttempts,
		MaxAudio:    cfg.SpeechMaxAudio,
		// A call may take its whole timeout, and the lease must outlast it or another worker could take the
		// recording while the first is still waiting.
		Lease: cfg.SpeechTimeout + 30*time.Second,
	})

	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(workerCtx)
	}()
	logger.Info("speech recognition is on", "service", cfg.SpeechURL, "timeout", cfg.SpeechTimeout, "max_attempts", cfg.SpeechMaxAttempts, "max_audio", cfg.SpeechMaxAudio)

	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			logger.Warn("the speech recognition worker did not stop in time")
		}
	}
	return &transcribe.Intake{MaxAudio: cfg.SpeechMaxAudio}, worker.Wake, stop, nil
}

// purgeExpired deletes expired sessions and pairings once an hour until ctx ends. Expired ones are already
// refused wherever they are used; this only keeps the tables from growing.
func purgeExpired(ctx context.Context, q dbgen.Querier, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			sessions, err := q.DeleteExpiredSessions(ctx, now)
			if err != nil && ctx.Err() == nil {
				logger.Warn("purge expired sessions", "error", err)
			}
			pairings, err := q.DeleteExpiredPairings(ctx, now)
			if err != nil && ctx.Err() == nil {
				logger.Warn("purge expired pairings", "error", err)
			}
			if sessions > 0 || pairings > 0 {
				logger.Info("expired entries removed", "sessions", sessions, "pairings", pairings)
			}
		}
	}
}

func migrate(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	if len(args) != 1 || (args[0] != "up" && args[0] != "status") {
		return errors.New("usage: pigeondrop migrate up|status")
	}

	pool, err := db.Connect(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()

	if args[0] == "up" {
		applied, err := db.MigrateUp(ctx, pool)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "applied %d migration(s)\n", applied)
		return nil
	}

	statuses, err := db.Status(ctx, pool)
	if err != nil {
		return err
	}
	for _, s := range statuses {
		fmt.Fprintf(stdout, "%05d  %-8s  %s\n", s.Source.Version, s.State, s.Source.Path)
	}
	return nil
}

// healthcheck asks the server on this machine whether it is healthy. Docker runs it
// as the container's HEALTHCHECK, because the final image has no shell, curl or wget.
func healthcheck(ctx context.Context, getenv func(string) string) error {
	port := getenv("PORT")
	if port == "" {
		port = "3000"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz answered %d", res.StatusCode)
	}
	return nil
}
