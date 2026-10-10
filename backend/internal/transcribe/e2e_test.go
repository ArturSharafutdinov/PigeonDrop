package transcribe

import (
	"os"
	"testing"
	"time"
)

// The whole chain on real parts: a real recording from the watch, the real WAV conversion, the HTTP client, the
// real service with the real model (stt/, started as in stt/README.md), and the queue in a real database.
// It is skipped unless the service is named:
//
//	STT_E2E_URL=http://127.0.0.1:18000 STT_E2E_TOKEN=... STT_E2E_FILE=/path/to/recording.opus \
//	STT_E2E_WANT='the exact text' go test -run EndToEnd ./internal/transcribe/
//
// STT_E2E_WANT is optional; without it the test only requires a non-empty text.
func TestEndToEndAgainstARunningService(t *testing.T) {
	url, token, file := os.Getenv("STT_E2E_URL"), os.Getenv("STT_E2E_TOKEN"), os.Getenv("STT_E2E_FILE")
	if url == "" || file == "" {
		t.Skip("STT_E2E_URL and STT_E2E_FILE are not set: no running service to try")
	}
	recording, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a recording becomes text", func(t *testing.T) {
		e := newEnv(t)
		id := e.message(withContent(recording), withDuration(6000))
		client, err := NewHTTP(url, token, 60*time.Second)
		if err != nil {
			t.Fatal(err)
		}

		started := time.Now()
		if !e.once(e.worker(client, Settings{})) {
			t.Fatal("nothing was taken from the queue")
		}

		row := e.row(id)
		if row.State != StateDone || row.Text == nil || *row.Text == "" {
			t.Fatalf("state = %s, text = %v, error = %v %v", row.State, row.Text, row.ErrorCode, row.LastError)
		}
		if want := os.Getenv("STT_E2E_WANT"); want != "" && *row.Text != want {
			t.Errorf("text = %q, want %q", *row.Text, want)
		}
		if row.Engine == nil || *row.Engine == "" || row.AudioMs == nil || *row.AudioMs <= 0 || row.SttMs == nil {
			t.Errorf("details: engine = %v, audio_ms = %v, stt_ms = %v", row.Engine, row.AudioMs, row.SttMs)
		}
		t.Logf("recognized in %s (the model took %d ms of %d ms of audio, engine %s): %q",
			time.Since(started).Round(time.Millisecond), *row.SttMs, *row.AudioMs, *row.Engine, *row.Text)
	})

	t.Run("a wrong token is the configuration's fault and costs the recording nothing", func(t *testing.T) {
		e := newEnv(t)
		id := e.message(withContent(recording), withDuration(6000))
		client, err := NewHTTP(url, "not-the-token", 60*time.Second)
		if err != nil {
			t.Fatal(err)
		}

		e.once(e.worker(client, Settings{}))

		row := e.row(id)
		if row.State != StatePending || row.Attempts != 0 || row.LastError == nil {
			t.Fatalf("state = %s, attempts = %d, last error = %v", row.State, row.Attempts, row.LastError)
		}
	})

	t.Run("a service that is not there is waited for", func(t *testing.T) {
		e := newEnv(t)
		id := e.message(withContent(recording), withDuration(6000))
		client, err := NewHTTP("http://127.0.0.1:9", token, 5*time.Second) // nothing listens on the discard port
		if err != nil {
			t.Fatal(err)
		}

		e.once(e.worker(client, Settings{}))

		if row := e.row(id); row.State != StatePending || row.Attempts != 0 {
			t.Fatalf("state = %s, attempts = %d", row.State, row.Attempts)
		}
	})

}
