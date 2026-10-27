package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// API is what the bot needs of Telegram; Client is the real one and the tests have a fake.
type API interface {
	GetMe(ctx context.Context) (Me, error)
	GetUpdates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error)
	SendMessage(ctx context.Context, p MessageParams) (Sent, error)
	SetMyCommands(ctx context.Context, commands []Command, languageCode string) error
	AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	RemoveButtons(ctx context.Context, chatID, messageID int64) error
}

var _ API = (*Client)(nil)

// TxFunc runs fn in one database transaction (db.InTx).
type TxFunc func(ctx context.Context, fn func(q dbgen.Querier) error) error

// BotSettings are the bot's settings; zero values mean the defaults.
type BotSettings struct {
	// PageURL is the page where a person signs in and links their chat; the bot names it in its answers.
	PageURL string
	// Poll is how long one getUpdates call waits for something to happen (default 30 s).
	Poll time.Duration
	// StartRetry and StartRetryMax are the first and the longest pause while the bot cannot find out who it is
	// (Telegram cannot be reached); the pause doubles (defaults 5 s and 5 minutes).
	StartRetry, StartRetryMax time.Duration
	// ErrorPause and ErrorPauseMax are the same for receiving messages (defaults 2 s and 1 minute).
	ErrorPause, ErrorPauseMax time.Duration
	// ConflictPause is the wait when another process receives the bot's messages (default 15 s).
	ConflictPause time.Duration
}

func (s BotSettings) withDefaults() BotSettings {
	if s.Poll <= 0 {
		s.Poll = 30 * time.Second
	}
	if s.StartRetry <= 0 {
		s.StartRetry = 5 * time.Second
	}
	if s.StartRetryMax < s.StartRetry {
		s.StartRetryMax = max(5*time.Minute, s.StartRetry)
	}
	if s.ErrorPause <= 0 {
		s.ErrorPause = 2 * time.Second
	}
	if s.ErrorPauseMax < s.ErrorPause {
		s.ErrorPauseMax = max(time.Minute, s.ErrorPause)
	}
	if s.ConflictPause <= 0 {
		s.ConflictPause = 15 * time.Second
	}
	return s
}

// Bot receives what people write to the bot and answers. It links a chat to an account when the person opens the
// link the page gave them and presses Start, unlinks it on /stop, and deletes a recording from the chat when the
// button under it is pressed (seen.go). Run it in a goroutine.
type Bot struct {
	api      API
	db       dbgen.Querier
	tx       TxFunc
	logger   *slog.Logger
	settings BotSettings
	// Now supplies the time; nil means time.Now. Tests set it.
	Now func() time.Time

	mu       sync.RWMutex
	username string
}

// NewBot returns a bot that talks through api and keeps its links in db.
func NewBot(api API, db dbgen.Querier, tx TxFunc, logger *slog.Logger, settings BotSettings) *Bot {
	return &Bot{api: api, db: db, tx: tx, logger: logger, settings: settings.withDefaults()}
}

func (b *Bot) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Username is the bot's name without the @, or empty until Telegram has said it. The page asks for it to make the
// link, and takes an empty answer to mean that the bot cannot be used yet.
func (b *Bot) Username() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.username
}

// Run works until ctx ends: it finds out who the bot is (waiting as long as it takes Telegram to be reachable), then
// receives messages and answers them.
func (b *Bot) Run(ctx context.Context) {
	if !b.identify(ctx) {
		return
	}
	b.setMenu(ctx)

	var offset int64
	var failures int
	pause := b.settings.ErrorPause
	for ctx.Err() == nil {
		updates, err := b.api.GetUpdates(ctx, offset, b.settings.Poll)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := b.afterPollError(err, failures, &pause)
			failures++
			if !sleep(ctx, wait) {
				return
			}
			continue
		}
		if failures > 0 {
			b.logger.Info("telegram: receiving messages again")
		}
		failures, pause = 0, b.settings.ErrorPause

		for _, update := range updates {
			offset = max(offset, update.UpdateID+1)
			switch {
			case update.Message != nil:
				b.handle(ctx, *update.Message)
			case update.CallbackQuery != nil:
				b.handleCallback(ctx, *update.CallbackQuery)
			}
		}
	}
}

// identify asks Telegram who the bot is until it answers; false means the server is stopping first.
func (b *Bot) identify(ctx context.Context) bool {
	pause := b.settings.StartRetry
	for first := true; ; first = false {
		me, err := b.api.GetMe(ctx)
		if err == nil {
			b.mu.Lock()
			b.username = me.Username
			b.mu.Unlock()
			b.logger.Info("telegram bot is on", "bot", "@"+me.Username)
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if first {
			b.logger.Error("telegram: cannot find out who the bot is; trying again (run `pigeondrop telegram check` to see why)", "error", err, "pause", pause)
		} else {
			b.logger.Debug("telegram: still cannot find out who the bot is", "error", err, "pause", pause)
		}
		if !sleep(ctx, pause) {
			return false
		}
		pause = min(pause*2, b.settings.StartRetryMax)
	}
}

// setMenu gives the bot its list of commands. A failure only costs the menu.
func (b *Bot) setMenu(ctx context.Context) {
	for _, l := range []struct {
		lang language
		code string
	}{{langEN, ""}, {langRU, "ru"}} {
		if err := b.api.SetMyCommands(ctx, menu(l.lang), l.code); err != nil && ctx.Err() == nil {
			b.logger.Warn("telegram: could not set the bot's menu", "error", err)
		}
	}
}

// afterPollError decides how long to wait after receiving failed, and says what happened (once per outage, in the
// log). pause carries the doubling from one failure to the next.
func (b *Bot) afterPollError(err error, failures int, pause *time.Duration) time.Duration {
	var failure *Error
	errors.As(err, &failure)
	switch {
	case failure != nil && failure.Kind == Conflict:
		if failures == 0 {
			b.logger.Warn("telegram: another process is receiving this bot's messages (an old copy of the server still running, or a webhook is set); waiting", "error", err)
		}
		return b.settings.ConflictPause
	case failure != nil && failure.Kind == RateLimited:
		return max(failure.RetryAfter, time.Second)
	}
	wait := *pause
	*pause = min(*pause*2, b.settings.ErrorPauseMax)
	if failures == 0 {
		b.logger.Warn("telegram: cannot receive messages; trying again", "error", err, "pause", wait)
	} else {
		b.logger.Debug("telegram: still cannot receive messages", "error", err, "pause", wait)
	}
	return wait
}

// sleep waits for d; false means ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// handle answers one message. Whatever goes wrong with it is dealt with here: one message must not stop the others.
func (b *Bot) handle(ctx context.Context, m Message) {
	if m.From == nil || m.From.IsBot {
		return
	}
	command, argument, ours := b.parseCommand(m.Text)
	isCommand := strings.HasPrefix(strings.TrimSpace(m.Text), "/")
	if isCommand && !ours {
		return // meant for another bot
	}
	if strings.TrimSpace(m.Text) == "" {
		return // a sticker, a photo, a voice message of theirs: nothing to answer
	}
	lang := languageOf(m.From.LanguageCode)
	page := b.settings.PageURL

	if m.Chat.Type != "private" {
		// In a group the bot only ever sees commands (privacy mode), and a chat with several people is not
		// somewhere a person's recordings may go.
		if isCommand {
			b.reply(ctx, m.Chat.ID, say(lang, phrasePrivateOnly, page))
		}
		return
	}

	switch command {
	case "/start":
		if argument == "" {
			b.reply(ctx, m.Chat.ID, say(lang, phraseHowTo, page))
			return
		}
		b.link(ctx, m, argument, lang)
	case "/stop":
		b.unlink(ctx, m, lang)
	default:
		b.reply(ctx, m.Chat.ID, say(lang, phraseHelp, page))
	}
}

// parseCommand splits "/start@thisbot code" into the command and what follows it. ours is false for a command
// addressed to another bot (in a group) and for text that is no command.
func (b *Bot) parseCommand(text string) (command, argument string, ours bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	head, rest, _ := strings.Cut(text, " ")
	head, target, addressed := strings.Cut(head, "@")
	if addressed && !strings.EqualFold(target, b.Username()) {
		return "", "", false
	}
	return strings.ToLower(head), strings.TrimSpace(rest), true
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if _, err := b.api.SendMessage(ctx, MessageParams{ChatID: chatID, Text: text}); err != nil && ctx.Err() == nil {
		// The person has gone, or Telegram is down; either way there is nobody to tell.
		b.logger.Debug("telegram: could not answer", "error", err)
	}
}

type linkOutcome int

const (
	linked linkOutcome = iota
	invalid
)

// link turns a press of Start with a code into a link between the chat and the account the code belongs to.
func (b *Bot) link(ctx context.Context, m Message, code string, lang language) {
	page := b.settings.PageURL
	if !ValidLinkCode(code) {
		b.reply(ctx, m.Chat.ID, say(lang, phraseInvalidLink, page))
		return
	}

	var outcome linkOutcome
	var userLang language = lang
	err := b.tx(ctx, func(q dbgen.Querier) error {
		now := b.now()
		hash := HashLinkCode(code)
		row, err := q.GetTelegramLinkCode(ctx, hash)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = invalid
			return nil
		}
		if err != nil {
			return fmt.Errorf("look up the link code: %w", err)
		}

		if row.UsedAt != nil {
			// The same press of Start arriving again (the bot was restarted while it handled it) is answered the
			// same way; anyone else holding a used code gets nothing.
			if row.UsedChatID != nil && *row.UsedChatID == m.Chat.ID {
				outcome = linked
				userLang = b.languageOf(ctx, q, row.UserID, lang)
			} else {
				outcome = invalid
			}
			return nil
		}

		used, err := q.UseTelegramLinkCode(ctx, dbgen.UseTelegramLinkCodeParams{CodeHash: hash, Now: now, ChatID: m.Chat.ID})
		if err != nil {
			return fmt.Errorf("use the link code: %w", err)
		}
		if used == 0 {
			outcome = invalid // expired
			return nil
		}
		if _, err := q.DeleteTelegramLinkByChat(ctx, m.Chat.ID); err != nil {
			return fmt.Errorf("free the chat: %w", err)
		}
		if err := q.UpsertTelegramLink(ctx, dbgen.UpsertTelegramLinkParams{
			UserID: row.UserID, ChatID: m.Chat.ID, Username: nonEmpty(m.From.Username), FirstName: nonEmpty(m.From.FirstName), Now: now,
		}); err != nil {
			return fmt.Errorf("store the link: %w", err)
		}
		outcome = linked
		userLang = b.languageOf(ctx, q, row.UserID, lang)
		b.logger.Info("telegram chat linked", "user_id", row.UserID)
		return nil
	})
	if err != nil {
		if ctx.Err() == nil {
			b.logger.Error("telegram: linking a chat failed", "error", err)
			b.reply(ctx, m.Chat.ID, say(lang, phraseTryAgain, page))
		}
		return
	}
	if outcome == linked {
		b.reply(ctx, m.Chat.ID, say(userLang, phraseLinked, page))
		return
	}
	b.reply(ctx, m.Chat.ID, say(lang, phraseInvalidLink, page))
}

// languageOf is the language of an account, or fallback when it cannot be read.
func (b *Bot) languageOf(ctx context.Context, q dbgen.Querier, userID uuid.UUID, fallback language) language {
	l, err := q.GetUserLanguage(ctx, userID)
	if err != nil {
		return fallback
	}
	return languageOf(l)
}

// unlink ends the link of the chat, whoever it was linked to.
func (b *Bot) unlink(ctx context.Context, m Message, lang language) {
	page := b.settings.PageURL
	row, err := b.db.GetTelegramLinkByChat(ctx, m.Chat.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		b.reply(ctx, m.Chat.ID, say(lang, phraseNotLinked, page))
		return
	}
	if err == nil {
		lang = b.languageOf(ctx, b.db, row.UserID, lang)
		_, err = b.db.DeleteTelegramLink(ctx, row.UserID)
	}
	if err != nil {
		if ctx.Err() == nil {
			b.logger.Error("telegram: unlinking a chat failed", "error", err)
			b.reply(ctx, m.Chat.ID, say(lang, phraseTryAgain, page))
		}
		return
	}
	b.logger.Info("telegram chat unlinked by the person", "user_id", row.UserID)
	b.reply(ctx, m.Chat.ID, say(lang, phraseStopped, page))
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
