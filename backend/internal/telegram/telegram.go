// Package telegram is the server's side of the Telegram bot: a client for the Bot API, and (in the files that
// follow it in the plan, TELEGRAM_BOT_PLAN.md) the bot that links a chat to an account and the queue that sends
// the recordings.
//
// The client knows nothing about users or recordings. What matters most to its callers is the way a call can fail,
// because a failure decides what happens to the recording being sent: Error.Kind says whether to wait, to give up
// on the chat, or to try again later (see Kind).
package telegram

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind says what a failed call means for whoever made it.
type Kind int

const (
	// Unavailable: the request never reached Telegram, or Telegram is not able to answer it now (no connection, a
	// proxy's 502/503/504, the token was not accepted: 401 or 404). Nobody is at fault, so the sender does not
	// count the attempt and waits.
	Unavailable Kind = iota + 1
	// RateLimited: too many messages (429); RetryAfter says how long to wait.
	RateLimited
	// Blocked: the chat cannot be written to any more (the person blocked the bot or deleted their account, the
	// chat does not exist). Trying again cannot help; the link to the chat is dead.
	Blocked
	// VoiceForbidden: the person's privacy settings do not allow voice messages. The same recording can be sent
	// as a file.
	VoiceForbidden
	// Rejected: Telegram refused this request as it is (a bad file, a text it cannot take). Sending it again
	// cannot help.
	Rejected
	// Conflict: another process is receiving the bot's updates with the same token (409).
	Conflict
	// Failed: the request was sent and the answer is missing or wrong (a timeout, a connection cut after the
	// request went out, a 500, an answer that is not what the Bot API says). Whether the message was delivered is
	// not known.
	Failed
)

func (k Kind) String() string {
	switch k {
	case Unavailable:
		return "unavailable"
	case RateLimited:
		return "rate_limited"
	case Blocked:
		return "blocked"
	case VoiceForbidden:
		return "voice_forbidden"
	case Rejected:
		return "rejected"
	case Conflict:
		return "conflict"
	case Failed:
		return "failed"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// Error is a failed call to the Bot API. Its text never contains the bot's token, which is part of every
// request's address and which Go's own network errors would otherwise print.
type Error struct {
	Kind Kind
	// Method is the Bot API method that failed, for example sendVoice.
	Method string
	// Code is Telegram's error_code, or the HTTP status when the answer did not have one; zero when there was
	// no answer.
	Code int
	// Description is Telegram's own words about the failure, for the operator's log. It is never shown to a person.
	Description string
	// RetryAfter is how long Telegram asked to wait (429); zero if it did not ask.
	RetryAfter time.Duration
	// Err is what happened when there was no answer to read (the network's error, with the token removed).
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "telegram %s: %s", e.Method, e.Kind)
	if e.Code != 0 {
		fmt.Fprintf(&b, " (%d)", e.Code)
	}
	if e.Description != "" {
		b.WriteString(": " + e.Description)
	}
	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf reports the kind of a failed call; ok is false for any other error (including a context ending).
func KindOf(err error) (kind Kind, ok bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind, true
	}
	return 0, false
}

// Me is the bot's own account, from getMe.
type Me struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// User is a person who wrote to the bot.
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
}

// Chat is where a message was written. Only the fields the bot uses are read.
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"` // private, group, supergroup or channel
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// Attachment is a voice message, an audio file or a document in a message that was sent.
type Attachment struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"` // seconds; voice and audio only
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

// Message is a message of the Bot API, with the fields the bot reads.
type Message struct {
	MessageID int64       `json:"message_id"`
	From      *User       `json:"from"`
	Chat      Chat        `json:"chat"`
	Text      string      `json:"text"`
	Voice     *Attachment `json:"voice"`
	Audio     *Attachment `json:"audio"`
	Document  *Attachment `json:"document"`
}

// Update is one thing that happened to the bot. Only messages are asked for.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// WebhookInfo is what getWebhookInfo says; while a webhook is set, getUpdates does not work.
type WebhookInfo struct {
	URL                string `json:"url"`
	PendingUpdateCount int    `json:"pending_update_count"`
	LastErrorMessage   string `json:"last_error_message"`
}

// Sent is what Telegram made of a message that was sent. Kind says what it is in Telegram's eyes - "voice" when
// the recording was accepted as a voice message, "audio" or "document" when it was not - which is the one thing
// about a file that the Bot API does not promise in advance.
type Sent struct {
	MessageID int64
	Kind      string
	// DurationSec is the length Telegram measured, for a voice message or an audio file.
	DurationSec int
	MimeType    string
}

// Command is an entry of the bot's menu (setMyCommands).
type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// VoiceParams is a recording to send as a voice message.
type VoiceParams struct {
	ChatID int64
	// Audio is an Ogg file encoded with Opus.
	Audio []byte
	// Caption goes under the message: at most MaxCaptionUnits.
	Caption string
	// Duration is the recording's length; Telegram works it out itself when it is zero.
	Duration time.Duration
}

// DocumentParams is a file to send as it is.
type DocumentParams struct {
	ChatID   int64
	Data     []byte
	FileName string
	Caption  string
}

// MessageParams is a text message.
type MessageParams struct {
	ChatID int64
	// Text is 1 to MaxMessageUnits.
	Text string
	// ReplyTo is the message this one answers; zero for none. A reply to a message that is gone is still sent.
	ReplyTo int64
}
