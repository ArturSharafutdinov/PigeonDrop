package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
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

// testEnv is the whole upload path on real parts: a real database, a real directory, the real handler.
type testEnv struct {
	handler http.Handler
	files   *storage.Disk
	db      dbgen.Querier
	pool    *pgxpool.Pool
	server  *Server
	logs    *syncBuffer

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
	files, err := storage.NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	e := &testEnv{files: files, pool: pool, logs: &syncBuffer{},
		clock: time.Date(2026, 10, 3, 7, 23, 36, 48*int(time.Millisecond), time.UTC)}
	srv := &Server{
		Pinger: pool,
		DB:     dbgen.New(pool),
		Files:  files,
		Logger: slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		// What an account without a server of its own resolves to.
		DefaultServerURL: defaultServerURL,
		// Each call to the clock is one millisecond later, so uploads get distinct, ordered names.
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

// post sends a raw request body to /messages and decodes the JSON answer.
func (e *testEnv) post(t testing.TB, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer is not JSON (%d): %q", rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

func (e *testEnv) upload(t testing.TB, fileName string, data []byte) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"fileName": fileName, "audio": base64.StdEncoding.EncodeToString(data)})
	return e.post(t, string(body))
}

func (e *testEnv) read(t testing.TB, name string) []byte {
	t.Helper()
	f, err := e.files.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *testEnv) names(t testing.TB) []string {
	t.Helper()
	entries, err := e.files.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	return out
}

var receivedName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}-\d{3}(-\d+)?\.opus$`)

func TestUploadStoresConvertsAndAnswers(t *testing.T) {
	for _, format := range []struct{ name, file string }{{"Ogg/Opus", "tone.ogg"}, {"Zepp framing", "tone.zepp"}} {
		t.Run(format.name, func(t *testing.T) {
			e := newEnv(t)
			data := fixture(t, format.file)

			status, body := e.upload(t, "voice_1.opus", data)
			if status != http.StatusOK || body["ok"] != true {
				t.Fatalf("got %d %v", status, body)
			}
			savedAs, _ := body["savedAs"].(string)
			if !receivedName.MatchString(savedAs) || savedAs != "2026-10-03_07-23-36-049.opus" {
				t.Errorf("savedAs = %q", savedAs)
			}
			if body["wavFile"] != "2026-10-03_07-23-36-049.wav" {
				t.Errorf("wavFile = %v", body["wavFile"])
			}
			if body["bytesReceived"] != float64(len(data)) {
				t.Errorf("bytesReceived = %v, want %d", body["bytesReceived"], len(data))
			}
			if _, err := time.Parse(time.RFC3339Nano, body["receivedAt"].(string)); err != nil {
				t.Errorf("receivedAt: %v", err)
			}

			if !bytes.Equal(e.read(t, savedAs), data) {
				t.Error("the stored recording is not byte-identical to what was sent")
			}
			if !bytes.Equal(e.read(t, body["wavFile"].(string)), fixture(t, "tone.wav")) {
				t.Error("the WAV is not what libopus produces for the same packets")
			}
		})
	}
}

func TestRepeatedUploadReturnsTheOriginalAndSavesNothingNew(t *testing.T) {
	e := newEnv(t)
	data := fixture(t, "tone.ogg")

	_, first := e.upload(t, "voice_1.opus", data)
	status, second := e.upload(t, "voice_1.opus", data)

	if status != http.StatusOK || second["savedAs"] != first["savedAs"] || second["wavFile"] != first["wavFile"] ||
		second["receivedAt"] != first["receivedAt"] || second["bytesReceived"] != first["bytesReceived"] {
		t.Fatalf("first %v, repeat %v", first, second)
	}
	if got := e.names(t); len(got) != 2 {
		t.Fatalf("a repeat must not store anything: %v", got)
	}
}

func TestSameFileNameWithDifferentContentIsStillTheSameUpload(t *testing.T) {
	// The file name is the idempotency key, as the contract says; the content is not compared.
	e := newEnv(t)
	_, first := e.upload(t, "voice_1.opus", fixture(t, "tone.ogg"))
	_, second := e.upload(t, "voice_1.opus", fixture(t, "tone.zepp"))
	if second["savedAs"] != first["savedAs"] || len(e.names(t)) != 2 {
		t.Fatalf("first %v, second %v, files %v", first, second, e.names(t))
	}
}

func TestConcurrentIdenticalUploadsStoreOneRecording(t *testing.T) {
	e := newEnv(t)
	data := fixture(t, "tone.ogg")

	const n = 12
	results := make([]map[string]any, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if status, body := e.upload(t, "voice_race.opus", data); status == http.StatusOK {
				results[i] = body
			}
		}()
	}
	wg.Wait()

	for i, r := range results {
		if r == nil || r["savedAs"] != results[0]["savedAs"] || r["receivedAt"] != results[0]["receivedAt"] {
			t.Fatalf("request %d got %v, request 0 got %v", i, r, results[0])
		}
	}
	// Whoever lost the race removed its own files: only the winner's recording and WAV remain.
	if got := e.names(t); len(got) != 2 {
		t.Fatalf("expected one recording and one WAV, found %v", got)
	}
}

func TestDamagedRecordingIsKeptButNotConverted(t *testing.T) {
	e := newEnv(t)
	damaged := bytes.Clone(fixture(t, "tone.ogg"))
	damaged[len(damaged)-10] ^= 0xFF

	status, body := e.upload(t, "voice_bad.opus", damaged)
	if status != http.StatusOK || body["ok"] != true || body["wavFile"] != nil {
		t.Fatalf("got %d %v", status, body)
	}
	if !bytes.Equal(e.read(t, body["savedAs"].(string)), damaged) {
		t.Error("the damaged recording must still be stored as received")
	}
	if got := e.names(t); len(got) != 1 {
		t.Errorf("no WAV should exist: %v", got)
	}
}

func TestRecordingCutMidPacketIsConvertedFromItsCompletePackets(t *testing.T) {
	e := newEnv(t)
	zepp := fixture(t, "tone.zepp")

	status, body := e.upload(t, "voice_cut.opus", zepp[:len(zepp)-5])
	if status != http.StatusOK || body["wavFile"] == nil {
		t.Fatalf("got %d %v", status, body)
	}
	if wav := e.read(t, body["wavFile"].(string)); len(wav) != 44+99*640 {
		t.Fatalf("WAV has %d bytes, want %d (99 packets)", len(wav), 44+99*640)
	}
}

func TestSixtySecondRecording(t *testing.T) {
	e := newEnv(t)
	// 30 copies of the two-second fixture's packets in Zepp framing = 60 s, about 180 KB.
	one := fixture(t, "tone.zepp")
	zepp := bytes.Repeat(one, 30)

	started := time.Now()
	status, body := e.upload(t, "voice_60.opus", zepp)
	if status != http.StatusOK || body["wavFile"] == nil {
		t.Fatalf("got %d %v", status, body)
	}
	if wav := e.read(t, body["wavFile"].(string)); len(wav) != 44+60*32000 {
		t.Fatalf("WAV has %d bytes, want %d", len(wav), 44+60*32000)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("took %v", took)
	}
}

func TestMalformedRequests(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("x", 256)

	for name, tc := range map[string]struct{ body, message string }{
		"not JSON":                 {`{broken`, "invalid JSON body"},
		"empty body":               {``, "invalid JSON body"},
		"JSON of the wrong shape":  {`[1,2]`, "invalid JSON body"},
		"fields of the wrong type": {`{"fileName": 5, "audio": true}`, "invalid JSON body"},
		"no audio":                 {`{"fileName":"a.opus"}`, "expected { fileName, audio }"},
		"no fileName":              {`{"audio":"AAAA"}`, "expected { fileName, audio }"},
		"empty strings":            {`{"fileName":"","audio":""}`, "expected { fileName, audio }"},
		"fileName too long":        {`{"fileName":"` + long + `","audio":"AAAA"}`, "fileName is too long"},
		"audio is not base64":      {`{"fileName":"a.opus","audio":"***"}`, "audio is not valid base64"},
		"audio decodes to nothing": {`{"fileName":"a.opus","audio":"\n\n"}`, "audio is empty"},
		"audio with a broken tail": {`{"fileName":"a.opus","audio":"AAAAA"}`, "audio is not valid base64"},
	} {
		status, body := e.post(t, tc.body)
		if status != http.StatusBadRequest || body["ok"] != false || body["error"] != tc.message {
			t.Errorf("%s: got %d %v, want 400 %q", name, status, body, tc.message)
		}
	}
	if got := e.names(t); len(got) != 0 {
		t.Errorf("a rejected request must store nothing: %v", got)
	}
}

func TestUploadLargerThanTheLimitIsRefused(t *testing.T) {
	e := newEnv(t)
	body := `{"fileName":"big.opus","audio":"` + strings.Repeat("A", MaxBodyBytes+1024) + `"}`

	status, answer := e.post(t, body)
	if status != http.StatusRequestEntityTooLarge || answer["error"] != "request body too large" {
		t.Fatalf("got %d %v", status, answer)
	}
}

func TestTwoUploadsInTheSameMillisecondDoNotCollide(t *testing.T) {
	e := newEnv(t)
	e.server.Now = func() time.Time { return time.Date(2026, 10, 3, 7, 23, 36, 48*int(time.Millisecond), time.UTC) }

	_, a := e.upload(t, "voice_a.opus", fixture(t, "tone.ogg"))
	_, b := e.upload(t, "voice_b.opus", fixture(t, "tone.zepp"))
	_, c := e.upload(t, "voice_c.opus", fixture(t, "tone.ogg"))

	if a["savedAs"] != "2026-10-03_07-23-36-048.opus" || b["savedAs"] != "2026-10-03_07-23-36-048-1.opus" || c["savedAs"] != "2026-10-03_07-23-36-048-2.opus" {
		t.Fatalf("got %v, %v, %v", a["savedAs"], b["savedAs"], c["savedAs"])
	}
	if !bytes.Equal(e.read(t, "2026-10-03_07-23-36-048.opus"), fixture(t, "tone.ogg")) {
		t.Error("the first recording was overwritten")
	}
}

func TestReceivedAtName(t *testing.T) {
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 3, 7, 23, 36, 48*int(time.Millisecond), time.UTC), "2026-10-03_07-23-36-048"},
		{time.Date(2026, 1, 2, 3, 4, 5, 5*int(time.Millisecond), time.UTC), "2026-01-02_03-04-05-005"},
		{time.Date(2026, 12, 31, 23, 59, 59, 999*int(time.Millisecond)+999999, time.UTC), "2026-12-31_23-59-59-999"},
		{time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC), "2026-06-07_08-09-10-000"},
	} {
		if got := receivedAtName(tc.at); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

// --- failures behind the handler ---

type brokenStore struct{ storage.Store }

func (brokenStore) Put(string, []byte) error { return errors.New("disk full") }

func TestStorageFailureIsAnErrorAndRecordsNothing(t *testing.T) {
	e := newEnv(t)
	e.server.Files = brokenStore{e.files}

	status, body := e.upload(t, "voice_1.opus", fixture(t, "tone.ogg"))
	if status != http.StatusInternalServerError || body["error"] != "internal error" {
		t.Fatalf("got %d %v", status, body)
	}
	// Nothing was recorded, so the watch's retry is a fresh attempt, not a "duplicate" of a failure.
	if _, err := e.db.GetProcessedUpload(context.Background(), "voice_1.opus"); err == nil {
		t.Fatal("a failed upload must not be remembered")
	}
}

// failsOnInsert lets the files be written, then fails the database write.
type failsOnInsert struct{ dbgen.Querier }

func (failsOnInsert) InsertProcessedUpload(context.Context, dbgen.InsertProcessedUploadParams) (dbgen.ProcessedUpload, error) {
	return dbgen.ProcessedUpload{}, errors.New("connection lost")
}

func TestDatabaseFailureLeavesNoOrphanFiles(t *testing.T) {
	e := newEnv(t)
	e.server.DB = failsOnInsert{e.db}

	status, _ := e.upload(t, "voice_1.opus", fixture(t, "tone.ogg"))
	if status != http.StatusInternalServerError {
		t.Fatalf("got %d", status)
	}
	if got := e.names(t); len(got) != 0 {
		t.Fatalf("files left behind: %v", got)
	}
}

func TestWAVHeaderOfAServedUploadIsConsistent(t *testing.T) {
	e := newEnv(t)
	_, body := e.upload(t, "voice_1.opus", fixture(t, "tone.zepp"))
	wav := e.read(t, body["wavFile"].(string))
	if binary.LittleEndian.Uint32(wav[40:]) != uint32(len(wav)-44) {
		t.Fatalf("data chunk size %d does not match the %d bytes that follow", binary.LittleEndian.Uint32(wav[40:]), len(wav)-44)
	}
}
