package httpapi

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The archive can be the whole 100 MiB of an account, more than the server's WriteTimeout allows on a slow link, so
// the handler moves the write deadline forward for each file. That goes through http.ResponseController, which
// only reaches the connection if every wrapper of the ResponseWriter on the way says how to get to the one under it
// (Unwrap). A ResponseRecorder has no connection and no deadline, so only a real server shows whether it works.
func TestAnArchiveTakingLongerThanTheWriteTimeoutIsNotCutOff(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	const files, perFile = 6, 4 << 20 // more than the socket buffers on both sides hold
	for i := range files {
		if rec := e.uploadAs(token, fmt.Sprintf("m%d", i), fixture(t, "tone.ogg")); rec.Code != http.StatusOK {
			t.Fatalf("upload %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	// The archive copies the stored files as they are, so they can be made as big as the test needs.
	big := make([]byte, perFile)
	for _, name := range e.storedNames(t) {
		if _, err := rand.Read(big); err != nil {
			t.Fatal(err)
		}
		if err := e.messages.Remove(name); err != nil { // Put does not overwrite
			t.Fatal(err)
		}
		if err := e.messages.Put(name, big); err != nil {
			t.Fatal(err)
		}
	}

	const writeTimeout = 300 * time.Millisecond
	srv := httptest.NewUnstartedServer(e.handler)
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/messages/archive", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(owner)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	// A slow link: the person's side does not take anything for several times the write timeout.
	time.Sleep(4 * writeTimeout)
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("the download was cut off after %d bytes, of %d: %v\nserver log:\n%s", len(body), files*perFile, err, e.logs.String())
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the archive is not whole (%d bytes): %v", len(body), err)
	}
	if len(zr.File) != files {
		t.Errorf("%d files in the archive, want %d", len(zr.File), files)
	}
	if strings.Contains(e.logs.String(), "could not extend") {
		t.Errorf("the write deadline could not be moved, though the server is a real one:\n%s", e.logs.String())
	}
}

// Where the connection cannot be reached (a ResponseWriter that says nothing about deadlines), the archive is still
// served, and the log says why a slow download may be cut off.
func TestAnArchiveWithoutADeadlineToMoveIsServedAndTheLogSaysSo(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	for i := range 3 {
		e.uploadAs(token, fmt.Sprintf("m%d", i), fixture(t, "tone.ogg"))
	}

	res := e.archiveOf(owner) // a ResponseRecorder: no connection, so no deadline

	if res.Code != http.StatusOK {
		t.Fatalf("status %d", res.Code)
	}
	if got := strings.Count(e.logs.String(), "could not extend"); got != 1 {
		t.Errorf("the log says it %d times, want once per download, not once per file:\n%s", got, e.logs.String())
	}
}
