package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
)

// The values of telegram_deliveries.state.
const (
	StatePending = "pending"
	StateRunning = "running"
	StateSent    = "sent"
	StateFailed  = "failed"
	StateSkipped = "skipped"
)

// The values of telegram_deliveries.error_code.
const (
	CodeBlocked         = "blocked"           // the chat blocked the bot or is gone
	CodeUnlinked        = "unlinked"          // the account was unlinked (or linked to another chat) before the recording was sent
	CodeRejected        = "rejected"          // Telegram refused the message and sending it again cannot help
	CodeTooManyFailures = "too_many_failures" // Telegram did not give a clear answer on every attempt
	CodeFileMissing     = "file_missing"      // the message exists but its file does not
	CodeNoAudio         = "no_audio"          // the recording is in the old Zepp format, which Telegram cannot play, and has no text to send instead
)

// DefaultBackoff is the pause before the next attempt after the first, second, ... failure of one delivery whose
// outcome is not known (see Failed); the last value repeats. An outage of Telegram, or its rate limit, has its own
// pauses and does not use up attempts.
var DefaultBackoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}

// maxFileBytes bounds what is read from a recording's file: uploads are limited to 512 KiB, so anything bigger is not
// a recording this server stored.
const maxFileBytes = 8 << 20

// Sender is what the delivery needs of Telegram; Client is the real one and the tests have a fake.
type Sender interface {
	SendVoice(ctx context.Context, p VoiceParams) (Sent, error)
	SendDocument(ctx context.Context, p DocumentParams) (Sent, error)
	SendMessage(ctx context.Context, p MessageParams) (Sent, error)
}

var _ Sender = (*Client)(nil)

// Intake is what the upload does with a new recording: it joins the queue of recordings for Telegram if its owner has a
// link that works, in the transaction that stores the message.
type Intake struct {
	// Wait is how long a delivery waits for the recording's text before it goes without it.
	Wait time.Duration
}

// Enqueue records a new message. q must belong to the transaction that inserted the message, so that the queue
// entry and the message appear (or do not) together. An account that is not linked gets no entry; repeating it for
// the same message changes nothing.
func (in Intake) Enqueue(ctx context.Context, q dbgen.Querier, messageID uuid.UUID, now time.Time) error {
	return q.EnqueueTelegramDelivery(ctx, dbgen.EnqueueTelegramDeliveryParams{MessageID: messageID, Now: now, WaitUntil: now.Add(in.Wait)})
}

// DeliverySettings are the settings of a Deliverer. Zero values mean the defaults (see withDefaults).
type DeliverySettings struct {
	// MaxAttempts is how many attempts that count a delivery gets; the last failure gives up (default 5).
	MaxAttempts int
	// Poll is how long an idle worker waits before it looks at the queue again (default 2 s); a new recording
	// wakes it sooner. It is also how soon a delivery that waited for its text notices that the text is ready.
	Poll time.Duration
	// Lease is how long a worker may hold a delivery before another may take it; it must be longer than the calls it
	// makes (default 2 minutes).
	Lease time.Duration
	// Backoff is the pause before the next attempt after each failure that counts (default DefaultBackoff).
	Backoff []time.Duration
	// OutageMin and OutageMax are the first and the longest pause while Telegram cannot be reached; the pause
	// doubles with each failed try (defaults 15 s and 5 minutes).
	OutageMin, OutageMax time.Duration
	// MinGap is the least time between two messages to one chat; Telegram allows about one a second (default 1 s).
	MinGap time.Duration
}

func (s DeliverySettings) withDefaults() DeliverySettings {
	if s.MaxAttempts <= 0 {
		s.MaxAttempts = 5
	}
	if s.Poll <= 0 {
		s.Poll = 2 * time.Second
	}
	if s.Lease <= 0 {
		s.Lease = 2 * time.Minute
	}
	if len(s.Backoff) == 0 {
		s.Backoff = DefaultBackoff
	}
	if s.OutageMin <= 0 {
		s.OutageMin = 15 * time.Second
	}
	if s.OutageMax < s.OutageMin {
		s.OutageMax = max(5*time.Minute, s.OutageMin)
	}
	if s.MinGap <= 0 {
		s.MinGap = time.Second
	}
	return s
}

// Deliverer takes recordings from the queue one at a time and sends them to the chat their owner linked. Run it in a
// goroutine; to run several, make one Deliverer for each (a Deliverer is not safe for concurrent use, the queue is).
type Deliverer struct {
	db       dbgen.Querier
	files    storage.Store
	api      Sender
	logger   *slog.Logger
	settings DeliverySettings
	// Now supplies the time written to the database; nil means time.Now. Tests set it.
	Now func() time.Time

	wake   chan struct{}
	outage outage
	// pausedUntil is when Telegram said we may send again (rate limit): nothing is taken before it.
	pausedUntil time.Time
	// lastSend is when each chat was last written to, to keep the gap between messages to one chat. Wall time, not Now.
	lastSend map[int64]time.Time
}

// NewDeliverer returns a worker for the queue in db, reading recordings from files and sending them through api.
func NewDeliverer(db dbgen.Querier, files storage.Store, api Sender, logger *slog.Logger, settings DeliverySettings) *Deliverer {
	return &Deliverer{db: db, files: files, api: api, logger: logger, settings: settings.withDefaults(), wake: make(chan struct{}, 1), lastSend: map[int64]time.Time{}}
}

func (d *Deliverer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Wake tells an idle worker that there is something new in the queue. It never blocks.
func (d *Deliverer) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run works until ctx ends. A delivery in hand when it ends is put back in the queue at once, with its attempt given
// back, so that stopping the server (a redeploy) costs the recording nothing.
func (d *Deliverer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		processed := false
		if now := d.now(); !d.outage.blocked(now) && !now.Before(d.pausedUntil) {
			var err error
			processed, err = d.RunOnce(ctx)
			if err != nil {
				if ctx.Err() == nil {
					d.logger.Error("telegram delivery queue", "error", err)
				}
				processed = false // do not spin on an error that repeats
			}
		}
		if processed {
			continue // there may be more waiting
		}
		select {
		case <-ctx.Done():
		case <-d.wake:
		case <-time.After(d.settings.Poll):
		}
	}
}

// RunOnce takes the oldest delivery that is ready and deals with it. It reports whether there was one. An error means
// the queue itself could not be used (the database); what happens to a delivery, good or bad, is not an error here.
func (d *Deliverer) RunOnce(ctx context.Context) (bool, error) {
	now := d.now()
	claimed, err := d.db.ClaimTelegramDelivery(ctx, dbgen.ClaimTelegramDeliveryParams{Now: now, LeaseUntil: now.Add(d.settings.Lease)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("take a recording from the queue: %w", err)
	}
	return true, d.process(ctx, claimed.MessageID, claimed.Attempts)
}

// splitText decides where a text goes: under the voice message if it fits there, otherwise as a message of its own.
func splitText(text string) (caption, message string) {
	switch {
	case text == "":
		return "", ""
	case Units(text) <= MaxCaptionUnits:
		return text, ""
	}
	return "", Fit(text, MaxMessageUnits)
}

func (d *Deliverer) process(ctx context.Context, id uuid.UUID, attempt int32) error {
	log := d.logger.With("message_id", id, "attempt", attempt)

	if int(attempt) > d.settings.MaxAttempts {
		// Taken again after its lease ran out, more often than a delivery may be tried: whatever happens to it in the
		// middle is not going to stop by trying again.
		log.Warn("giving up on a delivery whose attempts ran out without an answer")
		return d.fail(ctx, id, attempt, CodeTooManyFailures, "every earlier attempt ended without an answer")
	}

	subject, err := d.db.GetTelegramDeliverySubject(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Info("the recording was deleted before it was sent")
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up the recording: %w", err)
	}

	switch {
	case subject.LinkUserID == nil || subject.LinkChatID == nil:
		return d.skip(ctx, id, attempt, CodeUnlinked, "the account is not linked to a chat any more")
	case subject.LinkBlockedAt != nil:
		return d.skip(ctx, id, attempt, CodeBlocked, "the chat blocked the bot")
	case subject.DeliveryChatID != nil && *subject.DeliveryChatID != *subject.LinkChatID:
		// The voice message went to a chat the person has since unlinked; the rest is not sent to the new one.
		return d.skip(ctx, id, attempt, CodeUnlinked, "the account was linked to another chat after the recording was partly sent")
	}
	chat, userID := *subject.LinkChatID, *subject.LinkUserID

	text := ""
	if subject.TranscriptState != nil && *subject.TranscriptState == "done" && subject.TranscriptText != nil {
		text = strings.TrimSpace(*subject.TranscriptText)
	}
	caption, message := splitText(text)
	sendFailed := func(err error) error { return d.afterFailure(ctx, log, id, attempt, userID, chat, err) }

	voiceID := int64(0)
	if subject.VoiceMessageID != nil {
		voiceID = *subject.VoiceMessageID
	}

	switch {
	case voiceID != 0:
		// The voice message is already there (an earlier attempt sent it, and then the text that was to follow it
		// failed). Whatever text there is goes now as a message of its own: the voice message is not sent again, and
		// the text is not lost even if it has become short enough for a caption since.
		caption, message = "", Fit(text, MaxMessageUnits)
	case subject.Format != "ogg-opus":
		// A recording in the format the watch used to send cannot be played by Telegram. Its text can still be read.
		if text == "" {
			return d.skip(ctx, id, attempt, CodeNoAudio, "the recording is not an Ogg/Opus file and has no text")
		}
		caption, message = "", Fit(text, MaxMessageUnits)
	default:
		data, err := d.read(id)
		if errors.Is(err, storage.ErrNotFound) {
			// The recording may have been deleted just now (row and file go together); only a file missing under a row
			// that is still there is a problem.
			if _, err := d.db.GetTelegramDeliverySubject(ctx, id); errors.Is(err, pgx.ErrNoRows) {
				log.Info("the recording was deleted before it was sent")
				return nil
			}
			log.Error("the recording's file is missing")
			return d.fail(ctx, id, attempt, CodeFileMissing, "the file of the recording is missing")
		}
		if err != nil {
			return fmt.Errorf("read the recording: %w", err) // the lease runs out and the delivery is taken again
		}

		var duration time.Duration
		if subject.DurationMs != nil {
			duration = time.Duration(*subject.DurationMs) * time.Millisecond
		}
		started := time.Now()
		sent, err := d.send(ctx, chat, func() (Sent, error) {
			sent, err := d.api.SendVoice(ctx, VoiceParams{ChatID: chat, Audio: data, Caption: caption, Duration: duration})
			if kind, isCall := KindOf(err); isCall && kind == VoiceForbidden {
				// The person's privacy settings do not allow voice messages: the same recording goes as a file.
				log.Info("voice messages are not allowed in the chat; sending the recording as a file")
				return d.api.SendDocument(ctx, DocumentParams{ChatID: chat, Data: data, FileName: "recording.ogg", Caption: caption})
			}
			return sent, err
		})
		if err != nil {
			return sendFailed(err)
		}
		if sent.Kind != "voice" {
			log.Warn("telegram did not take the recording as a voice message", "taken_as", sent.Kind)
		}
		voiceID = sent.MessageID
		log.Debug("the recording is in telegram", "taken_as", sent.Kind, "ms", time.Since(started).Milliseconds())
		stored, err := d.db.RecordTelegramVoice(ctx, dbgen.RecordTelegramVoiceParams{MessageID: id, Attempt: attempt, ChatID: chat, VoiceMessageID: voiceID})
		if err != nil {
			return fmt.Errorf("remember the voice message: %w", err)
		}
		if stored == 0 {
			// The lease ran out and someone else has it, or the recording was deleted while it was being sent.
			log.Warn("the voice message was sent but the delivery is no longer ours")
			return nil
		}
	}

	if message != "" {
		if _, err := d.send(ctx, chat, func() (Sent, error) {
			return d.api.SendMessage(ctx, MessageParams{ChatID: chat, Text: message, ReplyTo: voiceID})
		}); err != nil {
			return sendFailed(err)
		}
	}

	withText := caption != "" || message != ""
	done, err := d.complete(ctx, id, attempt, chat, withText)
	if err != nil {
		return err
	}
	if done {
		log.Info("recording sent to telegram", "with_text", withText, "text_in_caption", caption != "")
	} else {
		log.Info("the recording was sent but the delivery is no longer ours")
	}
	return nil
}

// send waits for the gap Telegram wants between messages to one chat, makes the call, and notes the time of a call
// that was answered. An outage that ends here is noted in the log.
func (d *Deliverer) send(ctx context.Context, chat int64, call func() (Sent, error)) (Sent, error) {
	if wait := d.settings.MinGap - time.Since(d.lastSend[chat]); wait > 0 {
		if !sleep(ctx, wait) {
			return Sent{}, ctx.Err()
		}
	}
	sent, err := call()
	if err != nil {
		return Sent{}, err
	}
	if len(d.lastSend) > 256 {
		for c, at := range d.lastSend {
			if time.Since(at) > time.Minute {
				delete(d.lastSend, c)
			}
		}
	}
	d.lastSend[chat] = time.Now()
	if d.outage.recovered() {
		d.logger.Info("telegram is reachable again")
	}
	return sent, nil
}

// afterFailure decides what a failed call means for the delivery (see Kind).
func (d *Deliverer) afterFailure(ctx context.Context, log *slog.Logger, id uuid.UUID, attempt int32, userID uuid.UUID, chat int64, err error) error {
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		// The server is stopping. The delivery is put back as if it had never been taken.
		return d.release(ctx, id, attempt, 1, d.now(), "")
	}

	var failure *Error
	if !errors.As(err, &failure) {
		failure = &Error{Kind: Failed, Err: err} // a Sender that does not say: the safe reading is "it may have gone"
	}
	detail := failure.Error()
	now := d.now()

	switch failure.Kind {
	case Unavailable:
		pause, first := d.outage.failed(now, d.settings.OutageMin, d.settings.OutageMax)
		switch {
		case failure.Code == 401:
			log.Error("telegram does not accept the bot's token; waiting (check TELEGRAM_BOT_TOKEN)", "pause", pause, "error", detail)
		case first:
			log.Warn("telegram cannot be reached; waiting", "pause", pause, "error", detail)
		default:
			log.Debug("telegram still cannot be reached", "pause", pause, "error", detail)
		}
		return d.release(ctx, id, attempt, 1, now.Add(pause), detail)
	case RateLimited:
		d.outage.recovered() // it answered
		pause := max(failure.RetryAfter, time.Second)
		d.pausedUntil = now.Add(pause)
		log.Warn("telegram asked to slow down", "pause", pause)
		return d.release(ctx, id, attempt, 1, now.Add(pause), detail)
	case Blocked:
		d.outage.recovered()
		if _, err := d.db.BlockTelegramLink(ctx, dbgen.BlockTelegramLinkParams{UserID: userID, ChatID: chat, Now: now}); err != nil {
			return fmt.Errorf("switch off the link to a chat that blocked the bot: %w", err)
		}
		log.Info("the chat cannot be written to; its link is switched off until the person links again", "user_id", userID, "error", detail)
		return d.skip(ctx, id, attempt, CodeBlocked, detail)
	case Rejected, VoiceForbidden:
		d.outage.recovered()
		log.Warn("telegram refused the message", "error", detail)
		return d.fail(ctx, id, attempt, CodeRejected, detail)
	default: // Failed, Conflict
		d.outage.recovered()
		if int(attempt) >= d.settings.MaxAttempts {
			log.Warn("giving up on a delivery: telegram did not answer clearly on every attempt", "error", detail)
			return d.fail(ctx, id, attempt, CodeTooManyFailures, detail)
		}
		pause := pauseAfterFailure(d.settings.Backoff, int(attempt))
		log.Warn("no clear answer from telegram, so the message may or may not have arrived; trying the recording again later", "pause", pause, "error", detail)
		return d.release(ctx, id, attempt, 0, now.Add(pause), detail)
	}
}

// pauseAfterFailure is the wait before the next attempt, when attempt (counting from 1) has just failed.
func pauseAfterFailure(backoff []time.Duration, attempt int) time.Duration {
	if len(backoff) == 0 {
		return 0
	}
	return backoff[min(max(attempt-1, 0), len(backoff)-1)]
}

func (d *Deliverer) read(id uuid.UUID) ([]byte, error) {
	file, err := d.files.Open(storage.RecordingName(id))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("the file is over %d bytes", maxFileBytes)
	}
	return data, nil
}

// The ways a delivery leaves the running state. Each is fenced by the attempt: if the delivery is no longer this
// worker's, no row changes and the call is a no-op.

func (d *Deliverer) complete(ctx context.Context, id uuid.UUID, attempt int32, chat int64, withText bool) (bool, error) {
	ctx, done := finishing(ctx)
	defer done()
	n, err := d.db.CompleteTelegramDelivery(ctx, dbgen.CompleteTelegramDeliveryParams{MessageID: id, Attempt: attempt, WithText: withText, ChatID: &chat, Now: d.now()})
	if err != nil {
		return false, fmt.Errorf("mark the recording as sent: %w", err)
	}
	return n > 0, nil
}

func (d *Deliverer) fail(ctx context.Context, id uuid.UUID, attempt int32, code, detail string) error {
	ctx, done := finishing(ctx)
	defer done()
	_, err := d.db.FailTelegramDelivery(ctx, dbgen.FailTelegramDeliveryParams{
		MessageID: id, Attempt: attempt, ErrorCode: code, LastError: nonEmpty(shorten(detail)), Now: d.now(),
	})
	if err != nil {
		return fmt.Errorf("mark the delivery as failed: %w", err)
	}
	return nil
}

func (d *Deliverer) skip(ctx context.Context, id uuid.UUID, attempt int32, code, detail string) error {
	ctx, done := finishing(ctx)
	defer done()
	_, err := d.db.SkipTelegramDelivery(ctx, dbgen.SkipTelegramDeliveryParams{
		MessageID: id, Attempt: attempt, ErrorCode: code, LastError: nonEmpty(shorten(detail)), Now: d.now(),
	})
	if err != nil {
		return fmt.Errorf("mark the delivery as skipped: %w", err)
	}
	return nil
}

// release puts the delivery back in the queue. giveBack is 1 when the attempt must not count.
func (d *Deliverer) release(ctx context.Context, id uuid.UUID, attempt int32, giveBack int32, runAfter time.Time, detail string) error {
	ctx, done := finishing(ctx)
	defer done()
	_, err := d.db.ReleaseTelegramDelivery(ctx, dbgen.ReleaseTelegramDeliveryParams{
		MessageID: id, Attempt: attempt, GiveBack: giveBack, RunAfter: runAfter, LastError: nonEmpty(shorten(detail)),
	})
	if err != nil {
		return fmt.Errorf("put the delivery back in the queue: %w", err)
	}
	return nil
}

// finishing returns the context for a write that ends an attempt, and the function that releases it. Such a write
// must go through even when the server is being stopped (that is when the delivery has to be given back), so a context
// that has ended is replaced by one that ignores the cancellation, within a limit.
func finishing(ctx context.Context) (context.Context, func()) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// outage tracks the pauses while Telegram cannot be reached, so that it is asked about once in a while and not once for
// every recording in the queue.
type outage struct {
	pause time.Duration
	until time.Time
}

func (o *outage) blocked(now time.Time) bool { return !o.until.IsZero() && now.Before(o.until) }

// failed records one more failed try and returns the pause that follows; first is true for the first one of an outage
// (which is the one worth a warning in the log).
func (o *outage) failed(now time.Time, shortest, longest time.Duration) (pause time.Duration, first bool) {
	first = o.pause == 0
	if first {
		o.pause = shortest
	} else {
		o.pause = min(o.pause*2, longest)
	}
	o.until = now.Add(o.pause)
	return o.pause, first
}

// recovered ends an outage; it reports whether there was one.
func (o *outage) recovered() bool {
	was := o.pause > 0
	o.pause, o.until = 0, time.Time{}
	return was
}

// shorten fits an error text in the column meant for the operator.
func shorten(s string) string {
	const limit = 500
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}
