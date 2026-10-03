package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// request sends any method and body and decodes the JSON answer.
func (e *testEnv) request(t testing.TB, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: answer is not JSON (%d): %q", method, path, rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

func TestCreateAccountResolvesToThisServer(t *testing.T) {
	e := newEnv(t)
	status, a := e.request(t, http.MethodPost, "/accounts", `{"code":"m0x1y2z-ab12cd"}`)

	if status != http.StatusOK || a["ok"] != true || a["code"] != "m0x1y2z-ab12cd" ||
		a["customServerUrl"] != nil || a["serverUrl"] != defaultServerURL {
		t.Fatalf("got %d %v", status, a)
	}
	created, err := time.Parse(time.RFC3339Nano, a["createdAt"].(string))
	if err != nil || time.Since(created) > time.Minute || a["updatedAt"] != a["createdAt"] {
		t.Fatalf("timestamps: %v / %v (%v)", a["createdAt"], a["updatedAt"], err)
	}
}

func TestCreatingTwiceChangesNothing(t *testing.T) {
	e := newEnv(t)
	_, first := e.request(t, http.MethodPost, "/accounts", `{"code":"abc"}`)
	e.request(t, http.MethodPut, "/accounts/abc", `{"customServerUrl":"https://mine.example"}`)

	status, again := e.request(t, http.MethodPost, "/accounts", `{"code":"abc"}`)
	if status != http.StatusOK || again["customServerUrl"] != "https://mine.example" || again["serverUrl"] != "https://mine.example" {
		t.Fatalf("a repeated create overwrote the account: %d %v", status, again)
	}
	if again["createdAt"] != first["createdAt"] {
		t.Fatalf("createdAt moved: %v -> %v", first["createdAt"], again["createdAt"])
	}
}

func TestConcurrentCreatesOfOneCodeAllSucceedWithTheSameAccount(t *testing.T) {
	e := newEnv(t)
	const n = 12
	got := make([]map[string]any, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if status, a := e.request(t, http.MethodPost, "/accounts", `{"code":"race"}`); status == http.StatusOK {
				got[i] = a
			}
		}()
	}
	wg.Wait()
	for i, a := range got {
		if a == nil || a["createdAt"] != got[0]["createdAt"] {
			t.Fatalf("request %d: %v (request 0: %v)", i, a, got[0])
		}
	}
}

func TestReadAccount(t *testing.T) {
	e := newEnv(t)
	e.request(t, http.MethodPost, "/accounts", `{"code":"abc"}`)

	status, a := e.request(t, http.MethodGet, "/accounts/abc", "")
	if status != http.StatusOK || a["code"] != "abc" || a["serverUrl"] != defaultServerURL {
		t.Fatalf("got %d %v", status, a)
	}
	status, a = e.request(t, http.MethodGet, "/accounts/unknown", "")
	if status != http.StatusNotFound || a["ok"] != false || a["error"] != "account not found" {
		t.Fatalf("got %d %v", status, a)
	}
}

func TestSetAndResetTheAccountsServer(t *testing.T) {
	e := newEnv(t)
	_, created := e.request(t, http.MethodPost, "/accounts", `{"code":"abc"}`)

	status, a := e.request(t, http.MethodPut, "/accounts/abc", `{"customServerUrl":"https://my-own-server.example.com"}`)
	if status != http.StatusOK || a["customServerUrl"] != "https://my-own-server.example.com" || a["serverUrl"] != "https://my-own-server.example.com" {
		t.Fatalf("set: %d %v", status, a)
	}
	if a["createdAt"] != created["createdAt"] || a["updatedAt"] == created["updatedAt"] {
		t.Fatalf("createdAt must stay and updatedAt must move: %v / %v", a["createdAt"], a["updatedAt"])
	}
	if _, read := e.request(t, http.MethodGet, "/accounts/abc", ""); read["serverUrl"] != "https://my-own-server.example.com" {
		t.Fatalf("the change was not stored: %v", read)
	}

	status, a = e.request(t, http.MethodPut, "/accounts/abc", `{"customServerUrl":null}`)
	if status != http.StatusOK || a["customServerUrl"] != nil || a["serverUrl"] != defaultServerURL {
		t.Fatalf("reset: %d %v", status, a)
	}
}

func TestSettingTheServerIsValidated(t *testing.T) {
	e := newEnv(t)
	e.request(t, http.MethodPost, "/accounts", `{"code":"abc"}`)
	const message = "customServerUrl must be null or an http(s) URL"

	for name, body := range map[string]string{
		"missing field (not the same as null)": `{}`,
		"another scheme":                       `{"customServerUrl":"ftp://files.example"}`,
		"no scheme":                            `{"customServerUrl":"my-server.example"}`,
		"empty string":                         `{"customServerUrl":""}`,
		"a number":                             `{"customServerUrl":5}`,
		"a boolean":                            `{"customServerUrl":false}`,
		"an object":                            `{"customServerUrl":{"a":1}}`,
		"too long":                             `{"customServerUrl":"https://` + strings.Repeat("a", 3000) + `"}`,
	} {
		status, a := e.request(t, http.MethodPut, "/accounts/abc", body)
		if status != http.StatusBadRequest || a["ok"] != false || a["error"] != message {
			t.Errorf("%s: got %d %v", name, status, a)
		}
	}
	if status, a := e.request(t, http.MethodPut, "/accounts/abc", `{broken`); status != http.StatusBadRequest || a["error"] != "invalid JSON body" {
		t.Errorf("broken JSON: %d %v", status, a)
	}
	// none of that touched the account
	if _, a := e.request(t, http.MethodGet, "/accounts/abc", ""); a["customServerUrl"] != nil {
		t.Fatalf("a refused request changed the account: %v", a)
	}
}

func TestSettingTheServerNeverCreatesAnAccount(t *testing.T) {
	e := newEnv(t)
	status, a := e.request(t, http.MethodPut, "/accounts/nobody", `{"customServerUrl":"https://x.example"}`)
	if status != http.StatusNotFound || a["error"] != "account not found" {
		t.Fatalf("got %d %v", status, a)
	}
	if status, _ := e.request(t, http.MethodGet, "/accounts/nobody", ""); status != http.StatusNotFound {
		t.Fatal("the PUT created an account")
	}
}

func TestCreatingAnAccountNeedsACode(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct{ body, message string }{
		"no code":     {`{}`, "expected { code }"},
		"empty code":  {`{"code":""}`, "expected { code }"},
		"broken JSON": {`{broken`, "invalid JSON body"},
		"empty body":  {``, "invalid JSON body"},
		"too long":    {`{"code":"` + strings.Repeat("c", 129) + `"}`, "code is too long"},
	} {
		status, a := e.request(t, http.MethodPost, "/accounts", tc.body)
		if status != http.StatusBadRequest || a["ok"] != false || a["error"] != tc.message {
			t.Errorf("%s: got %d %v", name, status, a)
		}
	}
}

func TestCodesWithUnusualCharactersRoundTrip(t *testing.T) {
	e := newEnv(t)
	for _, code := range []string{"with space", "slash/inside", "ünï-çødé", "100%", "a+b?c=d#e"} {
		body, _ := json.Marshal(map[string]string{"code": code})
		if status, a := e.request(t, http.MethodPost, "/accounts", string(body)); status != http.StatusOK || a["code"] != code {
			t.Fatalf("create %q: %d %v", code, status, a)
		}
		if status, a := e.request(t, http.MethodGet, "/accounts/"+url.PathEscape(code), ""); status != http.StatusOK || a["code"] != code {
			t.Errorf("read %q: %d %v", code, status, a)
		}
	}
}
