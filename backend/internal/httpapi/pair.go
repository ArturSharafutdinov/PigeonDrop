package httpapi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"net/http"
)

// The page where a person signs in, pairs a watch and sees their recordings. It is a few small static files, no
// build step, standing in until the real front end exists. The routes are written by hand, not generated: they
// serve files, not the JSON API.

//go:embed pair.html
var pairHTML []byte

//go:embed pair.js
var pairJS []byte

// The checking and sealing of the "Personal server" tab, apart from the page so that it can be tried on its own
// (pair.relay.test.mjs).
//
//go:embed pair.relay.js
var pairRelayJS []byte

//go:embed pair.css
var pairCSS []byte

// The words of the page, one table per language. They are plain JSON so that they are easy to translate and to check
// (pair_test.go); the page gets both as one script, pair.texts.js, so that nothing is fetched and no inline code is
// needed under the strict policy below.
//
//go:embed pair.en.json
var pairTextsEN []byte

//go:embed pair.ru.json
var pairTextsRU []byte

var pairTextsJS = pairTexts(pairTextsEN, pairTextsRU)

func pairTexts(en, ru []byte) []byte {
	if !json.Valid(en) || !json.Valid(ru) {
		panic("httpapi: the texts of the pairing page are not valid JSON") // embedded files: a mistake of the code, caught by any test run
	}
	var script bytes.Buffer
	script.WriteString(`window.PAIR_TEXTS = {"en": `)
	script.Write(bytes.TrimSpace(en))
	script.WriteString(`, "ru": `)
	script.Write(bytes.TrimSpace(ru))
	script.WriteString("};\n")
	return script.Bytes()
}

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
