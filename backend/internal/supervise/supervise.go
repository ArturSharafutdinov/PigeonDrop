// Package supervise keeps a background loop of the server (the Telegram bot, the delivery worker, the speech
// recognition worker) from taking the whole process down: a panic in a goroutine of its own cannot be caught by the
// HTTP server's recovery, and an uncaught one ends the program, with every request in flight.
package supervise

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// Settings of Run. Zero values mean the defaults.
type Settings struct {
	// Pause is the wait before the first restart (default 1 s). It doubles with every panic that follows the
	// previous one soon, up to MaxPause (default 1 minute), so that a loop which panics at once is not restarted
	// in a tight spin that fills the log.
	Pause, MaxPause time.Duration
	// Stable is how long a run must last for its panic to count as a new problem: the pause starts over from Pause
	// instead of doubling (default 1 minute).
	Stable time.Duration

	// wait sleeps for d and reports false when ctx ended first; tests replace it.
	wait func(ctx context.Context, d time.Duration) bool
}

func (s Settings) withDefaults() Settings {
	if s.Pause <= 0 {
		s.Pause = time.Second
	}
	if s.MaxPause < s.Pause {
		s.MaxPause = max(time.Minute, s.Pause)
	}
	if s.Stable <= 0 {
		s.Stable = time.Minute
	}
	if s.wait == nil {
		s.wait = sleep
	}
	return s
}

// Run calls fn until it returns. A panic of fn is logged with its stack and fn is called again after a pause (see
// Settings); a plain return is the loop's own decision to end (it does that when ctx ends) and so ends Run too, as
// does ctx ending during a pause. name says in the log which loop it was.
//
// What a restarted loop starts from is its own business: the queue workers find their work in the database, where a
// recording whose handling panicked is given up on after its attempts run out (see Worker.process in
// internal/transcribe), and the Telegram bot keeps its position in the update stream in the Bot (see Bot.offset).
func Run(ctx context.Context, logger *slog.Logger, name string, s Settings, fn func(context.Context)) {
	s = s.withDefaults()
	pause := s.Pause
	for ctx.Err() == nil {
		started := time.Now()
		if !call(ctx, logger, name, fn) {
			return
		}
		if time.Since(started) >= s.Stable {
			pause = s.Pause
		}
		logger.Error("a background loop panicked and will be started again", "loop", name, "pause", pause)
		if !s.wait(ctx, pause) {
			return
		}
		pause = min(pause*2, s.MaxPause)
	}
}

// call runs fn once and reports whether it panicked.
func call(ctx context.Context, logger *slog.Logger, name string, fn func(context.Context)) (panicked bool) {
	defer func() {
		if v := recover(); v != nil {
			panicked = true
			logger.Error("panic in a background loop", "loop", name, "panic", v, "stack", string(debug.Stack()))
		}
	}()
	fn(ctx)
	return false
}

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
