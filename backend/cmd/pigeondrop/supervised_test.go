package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/config"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// A panic in a background loop is not caught by the HTTP server's recovery: in a goroutine of its own it ends the
// whole program. These tests give each loop a database that panics, the way a bug in the loop's own code would, and
// check that the process is still there and the loop is started again. Before the loops ran under supervise.Run,
// they crashed the test binary.

// panickingQueue is a database whose queue calls panic. Anything else a loop might call fails the same way, as the
// embedded interface is nil.
type panickingQueue struct {
	dbgen.Querier

	transcriptionClaims atomic.Int32
	deliveryClaims      atomic.Int32
	linkLookups         atomic.Int32
}

func (q *panickingQueue) ClaimTranscription(context.Context, dbgen.ClaimTranscriptionParams) (dbgen.ClaimTranscriptionRow, error) {
	q.transcriptionClaims.Add(1)
	panic("a bug in the speech recognition queue")
}

func (q *panickingQueue) ClaimTelegramDelivery(context.Context, dbgen.ClaimTelegramDeliveryParams) (dbgen.ClaimTelegramDeliveryRow, error) {
	q.deliveryClaims.Add(1)
	panic("a bug in the delivery queue")
}

func (q *panickingQueue) GetTelegramLinkByChat(context.Context, int64) (dbgen.TelegramLink, error) {
	q.linkLookups.Add(1)
	panic("a bug in the handling of /stop")
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gave up waiting for: %s", what)
}

func TestAPanicInTheSpeechRecognitionWorkerDoesNotStopTheServer(t *testing.T) {
	logs := &syncBuffer{}
	q := &panickingQueue{}
	cfg := config.Config{SpeechURL: "http://127.0.0.1:1", SpeechTimeout: time.Second, SpeechMaxAttempts: 5, SpeechMaxAudio: time.Minute}

	_, _, stop, err := startSpeechRecognition(context.Background(), cfg, q, nil, newLogger(logs, slog.LevelInfo))
	if err != nil {
		t.Fatal(err)
	}

	eventually(t, "the worker to be started again after its panic", func() bool { return q.transcriptionClaims.Load() >= 2 })
	stop()

	if out := logs.String(); !strings.Contains(out, "panic in a background loop") || !strings.Contains(out, "speech recognition worker") {
		t.Errorf("the panic was not logged with the loop's name:\n%s", out)
	}
}

// stubBotUpdates is a Bot API that knows one person and has one update for the bot: "/stop" from them. It keeps what
// the bot asked for.
type stubBotUpdates struct {
	*httptest.Server
	mu      sync.Mutex
	getMe   int
	offsets []int64
}

func newStubBotUpdates(t *testing.T) *stubBotUpdates {
	t.Helper()
	stub := &stubBotUpdates{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			stub.mu.Lock()
			stub.getMe++
			stub.mu.Unlock()
			io.WriteString(w, `{"ok":true,"result":`+botReply+`}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			var asked struct {
				Offset int64 `json:"offset"`
			}
			_ = json.Unmarshal(body, &asked)
			stub.mu.Lock()
			stub.offsets = append(stub.offsets, asked.Offset)
			stub.mu.Unlock()
			if asked.Offset == 0 {
				io.WriteString(w, `{"ok":true,"result":[{"update_id":7,"message":{"message_id":1,"from":{"id":100,"is_bot":false,"first_name":"Ann"},"chat":{"id":100,"type":"private"},"text":"/stop"}}]}`)
				return
			}
			<-r.Context().Done() // a long poll with nothing to say, as Telegram's is
		default: // setMyCommands and the like
			io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	t.Cleanup(stub.Close)
	return stub
}

func (s *stubBotUpdates) snapshot() (getMe int, offsets []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getMe, append([]int64(nil), s.offsets...)
}

func TestAPanicInTheTelegramLoopsDoesNotStopTheServerAndTheBotMovesPastTheUpdateThatCausedIt(t *testing.T) {
	logs := &syncBuffer{}
	q := &panickingQueue{}
	api := newStubBotUpdates(t)
	cfg := config.Config{
		TelegramToken: tgTestToken, TelegramAPIURL: api.URL, TelegramTimeout: 5 * time.Second, TelegramWait: time.Second,
		DefaultServerURL: "https://pigeon.example.com",
	}

	_, stop, err := startTelegram(context.Background(), cfg, q, nil, nil, newLogger(logs, slog.LevelInfo))
	if err != nil {
		t.Fatal(err)
	}

	// The delivery worker panics on every look at its queue; the bot panics on the update. Both come back.
	eventually(t, "the delivery worker to be started again", func() bool { return q.deliveryClaims.Load() >= 2 })
	eventually(t, "the bot to ask Telegram for updates again after its panic", func() bool {
		_, offsets := api.snapshot()
		return len(offsets) >= 2
	})
	stop()

	getMe, offsets := api.snapshot()
	if getMe < 2 {
		t.Errorf("the bot asked who it is %d time(s): it was not started again", getMe)
	}
	if q.linkLookups.Load() != 1 {
		t.Errorf("the update was handled %d times: the update that panicked must not be handed over again", q.linkLookups.Load())
	}
	// 0 for the first look, then 8: the update 7 that panicked is confirmed, so that Telegram forgets it.
	if offsets[0] != 0 || offsets[1] != 8 {
		t.Errorf("offsets asked for = %v, want 0 and then 8", offsets)
	}
	out := logs.String()
	for _, loop := range []string{"telegram bot", "telegram delivery worker"} {
		if !strings.Contains(out, "panic in a background loop") || !strings.Contains(out, loop) {
			t.Errorf("no panic logged for %q:\n%s", loop, out)
		}
	}
}
