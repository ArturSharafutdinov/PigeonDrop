package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/testdb"
)

const defaultServerURL = "https://pigeon.test"

// testEnv is the whole server on real parts: a real database, a real directory, the real handler.
type testEnv struct {
	handler  http.Handler
	messages *storage.Disk // where the recordings are stored
	db       dbgen.Querier
	pool     *pgxpool.Pool
	server   *Server
	logs     *syncBuffer

	mu    sync.Mutex
	clock time.Time
}

// syncBuffer collects the server's log; handlers write to it from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testHasherParams keep password hashing fast; the real settings are exercised in internal/auth.
var testHasherParams = auth.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1}

func newEnv(t *testing.T) *testEnv { return newEnvWith(t, nil) }

// newEnvWith is newEnv with a chance to change the Server before its handler is built.
func newEnvWith(t *testing.T, configure func(*Server)) *testEnv {
	t.Helper()
	pool := testdb.New(t)

	messages, err := storage.NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	e := &testEnv{messages: messages, pool: pool, logs: &syncBuffer{},
		clock: time.Date(2026, 10, 3, 7, 23, 36, 48*int(time.Millisecond), time.UTC)}
	srv := &Server{
		Pinger:   pool,
		DB:       dbgen.New(pool),
		Messages: messages,
		Logger:   slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		// The address the pairing page's link is built from.
		DefaultServerURL: defaultServerURL,
		// Each call to the clock is one millisecond later, so records get distinct, ordered times.
		Now: func() time.Time {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.clock = e.clock.Add(time.Millisecond)
			return e.clock
		},
		Tx:           db.InTx(pool),
		Hasher:       auth.NewHasher(testHasherParams, 4),
		CookieSecure: true,
	}
	if configure != nil {
		configure(srv)
	}
	e.server, e.db = srv, srv.DB
	e.handler = NewHandler(srv)
	return e
}

// advance moves the test clock forward.
func (e *testEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock = e.clock.Add(d)
}

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../audio/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
