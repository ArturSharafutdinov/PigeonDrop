// Package transcribe turns a recording into text by asking a speech recognition service (stt/ in the repository).
//
// The server's queue talks to the Transcriber interface only, so another service or provider is another
// implementation of it, and the queue's tests use a fake. What matters most to the queue is not the text but the
// way a call can fail: Error.Kind says whether the recording is at fault, which decides whether the attempt counts
// and whether to try again at all (see Kind).
package transcribe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Result is what a service made of one recording.
type Result struct {
	Text string
	// Language is what the model says it heard; empty when it does not say.
	Language string
	// Engine names what made the text (for example parakeet-tdt-0.6b-v3-int8), so that a text can always be traced
	// back to its model when the model is changed.
	Engine  string
	AudioMs int
	// SttMs is how long the model took.
	SttMs int
	// RequestID is the X-Request-Id of the call, the same string the service writes in its log.
	RequestID string
}

// Transcriber recognizes one recording.
type Transcriber interface {
	// Transcribe takes a 16 kHz, 16-bit, mono WAV (audio.ToWAV makes one).
	//
	// A failure of the call is an *Error. An error that is the caller's own context ending (shutdown, a deadline the
	// caller set) is returned as that context error and is not an *Error, so that the caller can tell "I was told to
	// stop" from "the service failed".
	Transcribe(ctx context.Context, wav []byte) (Result, error)
}

// Kind says what a failed call means for the recording.
type Kind int

const (
	// Unavailable: the request never reached the model, or the service is not able to work yet (connection
	// refused, name not found, the model is loading, a proxy's 502/503/504, a wrong address or token). The
	// recording is not at fault, so the queue does not count the attempt and waits for the service.
	Unavailable Kind = iota + 1
	// Busy: the service is recognizing another recording (429). Try again soon; not counted.
	Busy
	// Failed: the request went through and the answer is missing or wrong (a timeout, a connection cut after the
	// request was sent, a 500, an answer that is not what the protocol says). The recording itself may be the
	// cause, because a recording can make a model hang or crash, so the attempt counts: otherwise such a recording
	// would be tried forever.
	Failed
	// Rejected: the service refused the recording as it is (400, 413, 422). Sending it again cannot help.
	Rejected
)

func (k Kind) String() string {
	switch k {
	case Unavailable:
		return "unavailable"
	case Busy:
		return "busy"
	case Failed:
		return "failed"
	case Rejected:
		return "rejected"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// Error is a failed call.
type Error struct {
	Kind Kind
	// RetryAfter is what the service asked for in Retry-After; zero if it did not ask (or asked for something
	// that is not a number of seconds).
	RetryAfter time.Duration
	// RequestID is the call's X-Request-Id, to find the call in the service's log.
	RequestID string
	Err       error
}

func (e *Error) Error() string {
	if e.RequestID == "" {
		return fmt.Sprintf("transcription %s: %v", e.Kind, e.Err)
	}
	return fmt.Sprintf("transcription %s (request %s): %v", e.Kind, e.RequestID, e.Err)
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

// MaxTextRunes is the longest text that is stored. A minute of speech is a few hundred words; the limit only keeps a
// misbehaving service from filling the database.
const MaxTextRunes = 20000

// CleanText makes a service's text safe to store and show: invalid UTF-8 is replaced, NUL and other control
// characters are removed (PostgreSQL cannot store a NUL in text at all), line breaks are kept as "\n" and tabs
// become spaces, the ends are trimmed and the length is limited to MaxTextRunes.
func CleanText(text string) string {
	text = strings.ToValidUTF8(text, "�")
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text)

	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	cleaned := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(cleaned) > MaxTextRunes {
		runes := []rune(cleaned)
		cleaned = strings.TrimSpace(string(runes[:MaxTextRunes]))
	}
	return cleaned
}
