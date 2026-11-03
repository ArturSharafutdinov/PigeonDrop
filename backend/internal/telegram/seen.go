package telegram

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// The button under every recording in the chat ("Seen", or its Russian equivalent for a Russian-speaking chat). Pressing it deletes the recording's messages
// from the chat, so that the chat does not fill up with recordings that were already looked at. It deletes nothing on
// the server: the recording and its text stay on the page and in the archive.
//
// The button carries the id of the recording. The bot looks up there which messages of the chat belong to it (a voice
// message with the text as its caption, or a voice message and the text that followed it as a reply). A recording that
// is no longer on the server has no such record, and then the button deletes the message it was pressed under.

const seenPrefix = "seen:"

// SeenData is what the button under the recording id carries back to the bot: 41 bytes, and Telegram allows 64.
func SeenData(id uuid.UUID) string { return seenPrefix + id.String() }

func parseSeen(data string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(data, seenPrefix)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	if err != nil || id.String() != rest { // uuid.Parse also takes other spellings; the button only ever carries this one
		return uuid.Nil, false
	}
	return id, true
}

// handleCallback answers a press on a button. Telegram shows the person a progress bar until the press is answered,
// so every path ends in an answer.
func (b *Bot) handleCallback(ctx context.Context, q CallbackQuery) {
	page := b.settings.PageURL
	answer := func(text string, alert bool) {
		if err := b.api.AnswerCallbackQuery(ctx, q.ID, text, alert); err != nil && ctx.Err() == nil {
			b.logger.Debug("telegram: could not answer a button press", "error", err)
		}
	}

	id, ok := parseSeen(q.Data)
	// Only a press in the person's own chat counts: in a private chat the chat's id is the person's id.
	if !ok || q.From == nil || q.Message == nil || q.Message.Chat.ID != q.From.ID {
		answer("", false)
		return
	}
	chat, pressed := q.Message.Chat.ID, q.Message.MessageID
	lang := languageOf(q.From.LanguageCode)

	// The messages of the recording in this chat, other than the one pressed, which goes last: if that one cannot be
	// deleted, the person is told, and the others are already gone.
	var ids []int64
	row, err := b.db.GetTelegramDeliveryMessages(ctx, dbgen.GetTelegramDeliveryMessagesParams{MessageID: id, ChatID: chat})
	switch {
	case err == nil:
		lang = languageOf(row.UserLanguage)
		for _, other := range []*int64{row.VoiceMessageID, row.TextMessageID} {
			if other != nil && *other != pressed {
				ids = append(ids, *other)
			}
		}
	case errors.Is(err, pgx.ErrNoRows):
		// The recording is gone from the server, or went to another chat: only this message is known.
	default:
		if ctx.Err() != nil {
			return
		}
		b.logger.Warn("telegram: could not look up the messages of a recording", "error", err)
		answer(say(lang, phraseTryAgain, page), false)
		return
	}
	ids = append(ids, pressed)

	pressedStays := false
	for _, message := range ids {
		err := b.api.DeleteMessage(ctx, chat, message)
		if err == nil || AlreadyGone(err) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if kind, isCall := KindOf(err); isCall && kind == Rejected {
			// Telegram will not delete it: the message is more than 48 hours old.
			pressedStays = pressedStays || message == pressed
			continue
		}
		b.logger.Warn("telegram: could not delete a message from the chat", "error", err)
		answer(say(lang, phraseTryAgain, page), false)
		return
	}

	if pressedStays {
		// A button that offers what cannot be done is worse than none.
		if err := b.api.RemoveButtons(ctx, chat, pressed); err != nil && ctx.Err() == nil {
			b.logger.Debug("telegram: could not remove the button of an old message", "error", err)
		}
		answer(say(lang, phraseTooOld, page), true)
		return
	}
	answer("", false)
}
