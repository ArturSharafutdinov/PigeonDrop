package supervise

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder replaces the real sleeping: it remembers every pause and never waits.
type recorder struct {
	mu     sync.Mutex
	pauses []time.Duration
	cancel context.CancelFunc // called after this many pauses, if set
	after  int
}

func (r *recorder) wait(ctx context.Context, d time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pauses = append(r.pauses, d)
	if r.cancel != nil && len(r.pauses) >= r.after {
		r.cancel()
	}
	return ctx.Err() == nil
}

func logTo(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

func TestAPanicIsLoggedAndTheLoopStartedAgain(t *testing.T) {
	var logs bytes.Buffer
	rec := &recorder{}
	calls := 0

	Run(context.Background(), logTo(&logs), "test loop", Settings{wait: rec.wait}, func(context.Context) {
		calls++
		if calls < 3 {
			panic("boom")
		}
	})

	if calls != 3 {
		t.Errorf("the loop ran %d times, want 3 (two panics, then a normal end)", calls)
	}
	out := logs.String()
	if strings.Count(out, "panic in a background loop") != 2 || !strings.Contains(out, "boom") || !strings.Contains(out, "test loop") || !strings.Contains(out, "stack=") {
		t.Errorf("the log should name the loop, the panic and the stack, twice:\n%s", out)
	}
}

func TestThePauseDoublesUpToTheLimitWhilePanicsFollowEachOther(t *testing.T) {
	rec := &recorder{}
	calls := 0

	Run(context.Background(), logTo(&bytes.Buffer{}), "test loop",
		Settings{Pause: time.Second, MaxPause: 5 * time.Second, Stable: time.Hour, wait: rec.wait},
		func(context.Context) {
			calls++
			if calls < 6 {
				panic("again")
			}
		})

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	if len(rec.pauses) != len(want) {
		t.Fatalf("pauses = %v, want %v", rec.pauses, want)
	}
	for i := range want {
		if rec.pauses[i] != want[i] {
			t.Errorf("pauses = %v, want %v", rec.pauses, want)
			break
		}
	}
}

func TestALoopThatRanForAWhileStartsThePausesOver(t *testing.T) {
	rec := &recorder{}
	calls := 0

	Run(context.Background(), logTo(&bytes.Buffer{}), "test loop",
		Settings{Pause: time.Second, MaxPause: time.Minute, Stable: 20 * time.Millisecond, wait: rec.wait},
		func(context.Context) {
			calls++
			if calls < 4 {
				time.Sleep(30 * time.Millisecond) // longer than Stable: a new problem every time, not the same one again
				panic("after a long run")
			}
		})

	for i, d := range rec.pauses {
		if d != time.Second {
			t.Errorf("pause %d is %s, want %s: a panic after a stable run is not a tight loop", i, d, time.Second)
		}
	}
	if len(rec.pauses) != 3 {
		t.Errorf("pauses = %v, want 3 of them", rec.pauses)
	}
}

func TestANormalReturnEndsTheSupervision(t *testing.T) {
	rec := &recorder{}
	calls := 0

	Run(context.Background(), logTo(&bytes.Buffer{}), "test loop", Settings{wait: rec.wait}, func(context.Context) { calls++ })

	if calls != 1 || len(rec.pauses) != 0 {
		t.Errorf("calls = %d, pauses = %v: a loop that ended by itself must not be started again", calls, rec.pauses)
	}
}

func TestEndingTheContextDuringThePauseStopsTheRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &recorder{cancel: cancel, after: 1}
	calls := 0

	Run(ctx, logTo(&bytes.Buffer{}), "test loop", Settings{wait: rec.wait}, func(context.Context) {
		calls++
		panic("boom")
	})

	if calls != 1 {
		t.Errorf("the loop ran %d times after the server began to stop, want it not restarted", calls)
	}
}

func TestTheRealPauseIsCutShortWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	started := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, logTo(&bytes.Buffer{}), "test loop", Settings{Pause: time.Hour}, func(context.Context) {
			close(started)
			panic("boom")
		})
	}()
	<-started
	time.Sleep(20 * time.Millisecond) // let it get into the pause
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept waiting out an hour-long pause after its context ended")
	}
}

func TestTheContextIsPassedToTheLoop(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "yes")
	var got any

	Run(ctx, logTo(&bytes.Buffer{}), "test loop", Settings{}, func(c context.Context) { got = c.Value(key{}) })

	if got != "yes" {
		t.Errorf("the loop got another context")
	}
}
