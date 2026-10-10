package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/config"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// The operator's speech recognition commands, on a real database.

var sttNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// recording stores a message the way an upload does (without its file; the commands never read files) and returns its id.
func (c *cli) recording(name string, hasWav bool, durationMs int32, receivedAt time.Time) uuid.UUID {
	c.t.Helper()
	ctx := context.Background()
	user, err := c.q.CreateUser(ctx, dbgen.CreateUserParams{Email: name + "@example.com", PasswordHash: "x", Language: "ru", CreatedAt: sttNow})
	if err != nil {
		c.t.Fatal(err)
	}
	device, err := c.q.CreateDevice(ctx, dbgen.CreateDeviceParams{UserID: user.ID, Name: "watch", TokenHash: []byte(name), CreatedAt: sttNow})
	if err != nil {
		c.t.Fatal(err)
	}
	id := uuid.New()
	_, err = c.q.InsertMessage(ctx, dbgen.InsertMessageParams{
		ID: id, UserID: user.ID, DeviceID: device.ID, ClientMessageID: name, Format: "ogg-opus", BytesReceived: 1000,
		DurationMs: &durationMs, HasWav: hasWav, ReceivedAt: receivedAt,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return id
}

type queued struct {
	state    string
	attempts int
	code     *string
	enqueued time.Time
}

func (c *cli) entry(id uuid.UUID) (q queued, found bool) {
	c.t.Helper()
	rows, err := c.pool.Query(context.Background(), `SELECT state, attempts, error_code, enqueued_at FROM transcriptions WHERE message_id = $1`, id)
	if err != nil {
		c.t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return queued{}, false
	}
	if err := rows.Scan(&q.state, &q.attempts, &q.code, &q.enqueued); err != nil {
		c.t.Fatal(err)
	}
	return q, true
}

func (c *cli) exec(query string, args ...any) {
	c.t.Helper()
	if _, err := c.pool.Exec(context.Background(), query, args...); err != nil {
		c.t.Fatalf("%s: %v", query, err)
	}
}

func (c *cli) withSpeech(url string) {
	c.env = envOf(map[string]string{"DATABASE_URL": c.url, "STT_URL": url})
}

func TestSttEnqueueMissingQueuesOldRecordingsOldestFirstAndSkipsWhatCannotGo(t *testing.T) {
	c := newCLI(t)
	older := c.recording("older", true, 6000, sttNow.Add(-48*time.Hour))
	newer := c.recording("newer", true, 9000, sttNow.Add(-24*time.Hour))
	garbled := c.recording("garbled", false, 0, sttNow.Add(-36*time.Hour))
	long := c.recording("long", true, 90_000, sttNow.Add(-30*time.Hour))
	already := c.recording("already", true, 5000, sttNow.Add(-12*time.Hour))
	c.exec(`INSERT INTO transcriptions (message_id, state, run_after, enqueued_at, finished_at, text, engine)
	        VALUES ($1, 'done', now(), now(), now(), 'уже есть', 'm')`, already)

	stdout, stderr := c.mustRun("", "stt", "enqueue", "-missing")

	if !strings.Contains(stdout, "queued 2 recordings") || !strings.Contains(stdout, "marked 2 as not going to the model") {
		t.Errorf("stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "STT_URL is not set") {
		t.Errorf("no warning that nothing will work the queue: %q", stderr)
	}
	for name, want := range map[string]struct{ state, code string }{
		"older": {"pending", ""}, "newer": {"pending", ""}, "garbled": {"skipped", "not_decodable"}, "long": {"skipped", "too_long"},
	} {
		id := map[string]uuid.UUID{"older": older, "newer": newer, "garbled": garbled, "long": long}[name]
		got, found := c.entry(id)
		code := ""
		if got.code != nil {
			code = *got.code
		}
		if !found || got.state != want.state || code != want.code {
			t.Errorf("%s: %+v found=%v, want %+v", name, got, found, want)
		}
	}
	// The one that was already done is left alone.
	if got, _ := c.entry(already); got.state != "done" {
		t.Errorf("a finished recording was touched: %+v", got)
	}
	// Oldest first, so the backlog is worked in the order it was recorded.
	a, _ := c.entry(older)
	b, _ := c.entry(newer)
	if !a.enqueued.Before(b.enqueued) {
		t.Errorf("queued out of order: %v then %v", a.enqueued, b.enqueued)
	}
}

func TestSttEnqueueMissingTwiceQueuesNothingTheSecondTime(t *testing.T) {
	c := newCLI(t)
	c.withSpeech("http://stt:8000")
	c.recording("one", true, 5000, sttNow)

	c.mustRun("", "stt", "enqueue", "-missing")
	stdout, stderr := c.mustRun("", "stt", "enqueue", "-missing")

	if !strings.Contains(stdout, "queued 0 recordings") {
		t.Errorf("second run: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("a warning although STT_URL is set: %q", stderr)
	}
}

func TestSttEnqueueUsesTheConfiguredLimitUnlessAskedOtherwise(t *testing.T) {
	c := newCLI(t)
	c.env = envOf(map[string]string{"DATABASE_URL": c.url, "STT_URL": "http://stt:8000", "STT_MAX_AUDIO_SECONDS": "30"})
	short := c.recording("short", true, 20_000, sttNow)
	middle := c.recording("middle", true, 40_000, sttNow)

	c.mustRun("", "stt", "enqueue", "-missing")
	if got, _ := c.entry(short); got.state != "pending" {
		t.Errorf("short: %+v", got)
	}
	if got, _ := c.entry(middle); got.state != "skipped" {
		t.Errorf("middle (over the configured 30 s): %+v", got)
	}

	c.exec(`DELETE FROM transcriptions`)
	c.mustRun("", "stt", "enqueue", "-missing", "-max-audio", "60s")
	if got, _ := c.entry(middle); got.state != "pending" {
		t.Errorf("middle with -max-audio 60s: %+v", got)
	}
}

func TestSttEnqueueDoesNothingWithoutBeingToldToAndRefusesNonsense(t *testing.T) {
	c := newCLI(t)
	id := c.recording("one", true, 5000, sttNow)

	for _, args := range [][]string{{"stt", "enqueue"}, {"stt", "enqueue", "all"}, {"stt", "enqueue", "-missing", "-max-audio", "0s"}, {"stt", "enqueue", "-nope"}} {
		if _, _, err := c.run("", args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
	if _, found := c.entry(id); found {
		t.Error("a recording was queued by a command that was not asked to")
	}
}

func TestSttRetryFailedGivesFailedRecordingsAnotherTryAndLeavesTheRest(t *testing.T) {
	c := newCLI(t)
	c.withSpeech("http://stt:8000")
	early := c.recording("early", true, 5000, sttNow)
	late := c.recording("late", true, 5000, sttNow)
	skipped := c.recording("skipped", true, 5000, sttNow)
	done := c.recording("done", true, 5000, sttNow)
	waiting := c.recording("waiting", true, 5000, sttNow)
	c.exec(`INSERT INTO transcriptions (message_id, state, attempts, run_after, enqueued_at, finished_at, error_code, last_error)
	        VALUES ($1, 'failed', 5, now(), now() - interval '2 hours', now() - interval '90 minutes', 'too_many_failures', 'hung'),
	               ($2, 'failed', 5, now(), now() - interval '1 hour', now() - interval '30 minutes', 'rejected', 'no')`, early, late)
	c.exec(`INSERT INTO transcriptions (message_id, state, run_after, enqueued_at, finished_at, error_code)
	        VALUES ($1, 'skipped', now(), now(), now(), 'too_long')`, skipped)
	c.exec(`INSERT INTO transcriptions (message_id, state, run_after, enqueued_at, finished_at, text, engine)
	        VALUES ($1, 'done', now(), now(), now(), 'ok', 'm')`, done)
	c.exec(`INSERT INTO transcriptions (message_id, state, run_after, enqueued_at) VALUES ($1, 'pending', now(), now())`, waiting)

	stdout, stderr := c.mustRun("", "stt", "retry", "-failed")

	if !strings.Contains(stdout, "queued again: 2") || stderr != "" {
		t.Errorf("stdout %q, stderr %q", stdout, stderr)
	}
	for name, id := range map[string]uuid.UUID{"early": early, "late": late} {
		got, _ := c.entry(id)
		if got.state != "pending" || got.attempts != 0 || got.code != nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
	first, _ := c.entry(early)
	second, _ := c.entry(late)
	if !first.enqueued.Before(second.enqueued) {
		t.Errorf("the one that failed first should be tried first: %v, %v", first.enqueued, second.enqueued)
	}
	if got, _ := c.entry(skipped); got.state != "skipped" {
		t.Errorf("skipped: %+v", got)
	}
	if got, _ := c.entry(done); got.state != "done" {
		t.Errorf("done: %+v", got)
	}
	// Behind what was already waiting.
	w, _ := c.entry(waiting)
	if !w.enqueued.Before(first.enqueued) {
		t.Errorf("retried recordings jumped the queue: waiting %v, retried %v", w.enqueued, first.enqueued)
	}

	if _, _, err := c.run("", "stt", "retry"); err == nil {
		t.Error("retry without -failed did something")
	}
}

func TestSttStatusOnAnEmptyQueue(t *testing.T) {
	c := newCLI(t)
	var out bytes.Buffer

	if err := sttStatus(context.Background(), c.q, config.Config{}, sttNow, &out); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"recognition: off in this environment", "pending  0", "running  0", "done     0", "failed   0", "skipped  0",
		"waiting: nothing", "given up on in the last 24 hours: 0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "latest failure") {
		t.Errorf("a failure out of nothing:\n%s", out.String())
	}
}

func TestSttStatusShowsTheQueueAndTheLatestFailure(t *testing.T) {
	c := newCLI(t)
	for i, state := range []string{"pending", "pending", "running", "done", "done", "done", "failed", "failed", "skipped"} {
		id := c.recording("m"+string(rune('a'+i)), true, 5000, sttNow)
		switch state {
		case "pending":
			c.exec(`INSERT INTO transcriptions (message_id, state, run_after, enqueued_at) VALUES ($1, 'pending', $2, $2)`, id, sttNow.Add(-time.Duration(i+1)*10*time.Minute))
		case "running":
			c.exec(`INSERT INTO transcriptions (message_id, state, attempts, run_after, enqueued_at, locked_until) VALUES ($1, 'running', 1, $2, $2, $3)`, id, sttNow.Add(-time.Hour), sttNow.Add(time.Minute))
		case "done":
			c.exec(`INSERT INTO transcriptions (message_id, state, run_after, enqueued_at, finished_at, text, engine) VALUES ($1, 'done', $2, $2, $2, 't', 'm')`, id, sttNow.Add(-time.Hour))
		case "failed":
			finished := sttNow.Add(-time.Duration(i-5) * 20 * time.Hour) // 20 h and 40 h ago
			c.exec(`INSERT INTO transcriptions (message_id, state, attempts, run_after, enqueued_at, finished_at, error_code, last_error) VALUES ($1, 'failed', 5, $2, $2, $2, 'too_many_failures', $3)`,
				id, finished, "the model hung on "+state)
		case "skipped":
			c.exec(`INSERT INTO transcriptions (message_id, state, run_after, enqueued_at, finished_at, error_code) VALUES ($1, 'skipped', $2, $2, $2, 'too_long')`, id, sttNow)
		}
	}
	var out bytes.Buffer

	if err := sttStatus(context.Background(), c.q, config.Config{SpeechURL: "http://stt:8000"}, sttNow, &out); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	for _, want := range []string{"recognition: on in this environment", "pending  2", "running  1", "done     3", "failed   2", "skipped  1",
		"oldest waiting since 2026-10-10 11:00:00 UTC (1h0m0s ago)", // the running one counts as waiting
		"given up on in the last 24 hours: 1", "latest failure: 2026-10-09 16:00 UTC, too_many_failures",
		"the model hung on failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
