package httpapi

import (
	"bytes"
	"errors"
	"html/template"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	_ "embed"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/audio"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
)

// The routes in this file are for people with a browser. They are written by hand rather than
// generated (see the "manual" tag in api/openapi.yaml) because they serve HTML and files, with
// range requests so that a browser's audio player can seek.

//go:embed setup.html
var setupPage []byte

func (s *Server) handleSetup(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(setupPage)
}

var uploadsPage = template.Must(template.New("uploads").Parse(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>PigeonDrop uploads</title></head>
<body>
<h1>Uploads ({{len .Names}})</h1>
<ul>{{range .Names}}<li><a href="/uploads/{{.}}">{{.}}</a></li>
{{else}}<li>(empty)</li>{{end}}</ul>
</body>
</html>
`))

// handleListUploads lists the stored recordings and WAV files, newest first. Their names start with the
// time the server received them, so reverse name order is newest first without parsing anything.
func (s *Server) handleListUploads(w http.ResponseWriter, _ *http.Request) {
	entries, err := s.Files.List()
	if err != nil {
		s.Logger.Error("list uploads", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name, ".wav") || strings.HasSuffix(entry.Name, ".opus") {
			names = append(names, entry.Name)
		}
	}
	slices.Reverse(names)

	var page bytes.Buffer
	if err := uploadsPage.Execute(&page, struct{ Names []string }{names}); err != nil {
		s.Logger.Error("render uploads page", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page.Bytes())
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !storage.ValidName(name) {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	file, err := s.Files.Open(name)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "file not found")
		return
	case err != nil:
		s.Logger.Error("open upload", "file", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer file.Close()

	contentType, err := contentTypeOf(name, file)
	if err != nil {
		s.Logger.Error("read upload", "file", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, time.Time{}, file)
}

// contentTypeOf looks inside a .opus file rather than trusting its name: a recording from the
// current app is a standard Ogg/Opus file a browser can play, one from an older app is Zepp's
// private framing it cannot. The file is rewound afterwards.
func contentTypeOf(name string, file io.ReadSeeker) (string, error) {
	switch {
	case strings.HasSuffix(name, ".wav"):
		return "audio/wav", nil
	case strings.HasSuffix(name, ".opus"):
		head := make([]byte, 4)
		n, err := io.ReadFull(file, head)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return "", err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		if audio.Detect(head[:n]) == audio.FormatOggOpus {
			return "audio/ogg", nil
		}
	}
	return "application/octet-stream", nil
}
