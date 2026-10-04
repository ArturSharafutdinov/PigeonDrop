package httpapi

import (
	_ "embed"
	"net/http"
)

// The page where a person signs in, pairs a watch and sees their recordings. It is three small static files, no
// build step, standing in until the real front end exists. The routes are written by hand, not generated: they
// serve files, not the JSON API.

//go:embed pair.html
var pairHTML []byte

//go:embed pair.js
var pairJS []byte

//go:embed pair.css
var pairCSS []byte

// pageSecurityHeaders locks the page down: it may load only its own script and style, talk only to this server,
// and never be framed. The pairing code is in the address, so the page also never sends a Referer.
func pageSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy",
		"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; media-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", "no-cache")
}

func servePage(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		pageSecurityHeaders(w.Header())
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}
