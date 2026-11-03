package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What the /v1 API answers is about one person's account, sessions and recordings: no browser or proxy may keep it.
func TestEveryAnswerOfTheV1APIIsNotToBeStored(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	e.uploadAs(token, "one", fixture(t, "tone.ogg"))

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"who am I":                 e.me(owner),
		"who am I, not signed in":  e.me(nil),
		"list of recordings":       e.listMessages(owner, ""),
		"sign-in refused":          e.login("nobody@example.com", "not a password at all"),
		"a body that is no JSON":   e.call(http.MethodPost, "/v1/auth/login", "{"),
		"a path nobody serves":     e.call(http.MethodGet, "/v1/nothing-here", ""),
		"a method nobody serves":   e.call(http.MethodDelete, "/v1/me", ""),
		"a foreign web page":       e.call(http.MethodPost, "/v1/auth/login", `{}`, withHeader("Sec-Fetch-Site", "cross-site")),
		"the device flow":          e.startPairing("watch"),
		"sign-in with a big body":  e.call(http.MethodPost, "/v1/auth/login", `{"email":"`+strings.Repeat("a", MaxBodyBytes)+`"}`),
		"the storage of a watch":   e.call(http.MethodGet, "/v1/storage", "", bearer(token)),
		"the settings of the user": e.call(http.MethodGet, "/v1/me", "", withCookie(owner)),
	} {
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: %d with Cache-Control %q, want no-store", name, rec.Code, got)
		}
	}
}

// The handlers that serve files have a policy of their own, which the general one must not replace: the player
// revalidates its recording, and the archive says no-store in its own words.
func TestFilesKeepTheirOwnCachePolicy(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	e.uploadAs(token, "one", fixture(t, "tone.ogg"))
	id := bodyOf(t, e.listMessages(owner, ""))["messages"].([]any)[0].(map[string]any)["id"].(string)

	if got := e.audioOf(owner, id, "").Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("audio: Cache-Control %q", got)
	}
	if got := e.archiveOf(owner).Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("archive: Cache-Control %q", got)
	}
}
