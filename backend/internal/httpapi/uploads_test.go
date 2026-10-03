package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func (e *testEnv) get(t testing.TB, path string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func TestSetupPage(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/setup", "/setup?code=m0x1y2z-ab12cd"} {
		rec := e.get(t, path)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") ||
			!bytes.Equal(rec.Body.Bytes(), setupPage) || !strings.Contains(rec.Body.String(), "<html") {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

func TestUploadsListIsNewestFirstAndLinksWork(t *testing.T) {
	e := newEnv(t)

	empty := e.get(t, "/uploads")
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), "Uploads (0)") || !strings.Contains(empty.Body.String(), "(empty)") {
		t.Fatalf("empty list: %d %q", empty.Code, empty.Body.String())
	}

	_, first := e.upload(t, "voice_1.opus", fixture(t, "tone.ogg"))
	_, second := e.upload(t, "voice_2.opus", fixture(t, "tone.zepp"))

	rec := e.get(t, "/uploads")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(body, "Uploads (4)") {
		t.Fatalf("got %d %q", rec.Code, body)
	}

	links := regexp.MustCompile(`href="/uploads/([^"]+)"`).FindAllStringSubmatch(body, -1)
	var order []string
	for _, l := range links {
		order = append(order, l[1])
	}
	want := []string{second["wavFile"].(string), second["savedAs"].(string), first["wavFile"].(string), first["savedAs"].(string)}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Fatalf("order %v, want newest first %v", order, want)
	}
}

func TestUploadsListIgnoresForeignFiles(t *testing.T) {
	e := newEnv(t)
	_ = e.files.Put("notes.txt", []byte("x"))
	_ = e.files.Put("2026-10-03_07-00-00-000.wav", []byte("RIFF"))

	body := e.get(t, "/uploads").Body.String()
	if strings.Contains(body, "notes.txt") || !strings.Contains(body, "Uploads (1)") {
		t.Fatalf("got %q", body)
	}
}

func TestDownloadContentTypes(t *testing.T) {
	e := newEnv(t)
	_, ogg := e.upload(t, "voice_ogg.opus", fixture(t, "tone.ogg"))
	_, zepp := e.upload(t, "voice_zepp.opus", fixture(t, "tone.zepp"))

	for _, tc := range []struct {
		name, contentType string
		want              []byte
	}{
		{ogg["savedAs"].(string), "audio/ogg", fixture(t, "tone.ogg")},
		{ogg["wavFile"].(string), "audio/wav", fixture(t, "tone.wav")},
		{zepp["savedAs"].(string), "application/octet-stream", fixture(t, "tone.zepp")}, // not claimed to be audio
	} {
		rec := e.get(t, "/uploads/"+tc.name)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != tc.contentType {
			t.Errorf("%s: %d %q, want %q", tc.name, rec.Code, rec.Header().Get("Content-Type"), tc.contentType)
		}
		if !bytes.Equal(rec.Body.Bytes(), tc.want) {
			t.Errorf("%s: wrong content", tc.name)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", tc.name)
		}
	}
}

func TestDownloadSupportsRangeRequests(t *testing.T) {
	e := newEnv(t)
	_, ogg := e.upload(t, "voice_1.opus", fixture(t, "tone.ogg"))
	wav := fixture(t, "tone.wav")

	rec := e.get(t, "/uploads/"+ogg["wavFile"].(string), "Range", "bytes=44-99")
	if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), wav[44:100]) {
		t.Fatalf("got %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Range"), "bytes 44-99/") {
		t.Fatalf("Content-Range = %q", rec.Header().Get("Content-Range"))
	}
}

func TestDownloadRefusesBadNamesAndMissingFiles(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		path    string
		status  int
		message string
	}{
		{"/uploads/..%2Fserver.js", http.StatusBadRequest, "invalid filename"},
		{"/uploads/..%2F..%2Fetc%2Fpasswd", http.StatusBadRequest, "invalid filename"},
		{"/uploads/.hidden", http.StatusBadRequest, "invalid filename"},
		{"/uploads/a..b.opus", http.StatusBadRequest, "invalid filename"},
		{"/uploads/bad%20name.opus", http.StatusBadRequest, "invalid filename"},
		{"/uploads/" + strings.Repeat("a", 300), http.StatusBadRequest, "invalid filename"},
		{"/uploads/nothing.wav", http.StatusNotFound, "file not found"},
	} {
		rec := e.get(t, tc.path)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != tc.status || !strings.Contains(string(body), tc.message) || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s: got %d %q", tc.path, rec.Code, body)
		}
	}
}
