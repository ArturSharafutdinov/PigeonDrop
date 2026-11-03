package httpapi

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestThePairingPageIsServedWithAStrictPolicy(t *testing.T) {
	e := newEnv(t)
	for path, contentType := range map[string]string{
		"/pair":          "text/html; charset=utf-8",
		"/pair.js":       "text/javascript; charset=utf-8",
		"/pair.texts.js": "text/javascript; charset=utf-8",
		"/pair.css":      "text/css; charset=utf-8",
	} {
		rec := e.call(http.MethodGet, path, "")
		h := rec.Header()
		if rec.Code != http.StatusOK || h.Get("Content-Type") != contentType || rec.Body.Len() == 0 {
			t.Errorf("%s: %d %q", path, rec.Code, h.Get("Content-Type"))
		}
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'", "form-action 'none'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: the policy lacks %q: %s", path, want, csp)
			}
		}
		if strings.Contains(csp, "unsafe-") || strings.Contains(csp, "*") || strings.Contains(csp, "http") {
			t.Errorf("%s: the policy is looser than intended: %s", path, csp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") == "" {
			t.Errorf("%s: headers %v", path, h)
		}
	}
	// Only GET: the page takes no input.
	if rec := e.call(http.MethodPost, "/pair", "{}"); rec.Code == http.StatusOK {
		t.Errorf("POST /pair answered 200")
	}
}

// The policy forbids inline script and inline style, so a page that used them would silently stop working.
func TestThePairingPageHasNothingThePolicyWouldBlock(t *testing.T) {
	html := string(pairHTML)
	if m := regexp.MustCompile(`(?s)<script\b[^>]*>(.*?)</script>`).FindAllStringSubmatch(html, -1); len(m) != 2 || strings.TrimSpace(m[0][1]) != "" || strings.TrimSpace(m[1][1]) != "" {
		t.Errorf("the page must have exactly two scripts (the texts and the code), with no inline code: %q", m)
	}
	if !strings.Contains(html, `<script src="/pair.texts.js">`) || !strings.Contains(html, `<script src="/pair.js">`) || !strings.Contains(html, `href="/pair.css"`) {
		t.Error("the page does not load its script and style from this server")
	}
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(html) {
		t.Error("an inline event handler")
	}
	if regexp.MustCompile(`(?i)\sstyle\s*=`).MatchString(html) || strings.Contains(strings.ToLower(html), "<style") {
		t.Error("inline style")
	}
	// Nothing is loaded from another host. The one thing allowed is a link a person may follow: the licence of the
	// speech recognition model, which asks for its name and a link to it (CC BY 4.0).
	licence := regexp.MustCompile(`<a href="https://creativecommons\.org/licenses/by/4\.0/[a-z/.-]*" rel="noopener">CC BY 4\.0</a>`)
	if n := len(licence.FindAllString(html, -1)); n != 1 {
		t.Errorf("the model's licence link is on the page %d times, want once", n)
	}
	if regexp.MustCompile(`https?://`).MatchString(licence.ReplaceAllString(html, "") + string(pairJS) + string(pairCSS) + string(pairTextsJS)) {
		t.Error("the page refers to another host")
	}
	// Nothing from the network may be written into the page as markup.
	if regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\(|new Function`).MatchString(string(pairJS)) {
		t.Error("the script writes markup or evaluates text")
	}
}

// Every element the script looks up must exist: a typo would break the page without any other test noticing.
func TestEveryElementTheScriptUsesIsOnThePage(t *testing.T) {
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`\sid="([^"]+)"`).FindAllStringSubmatch(string(pairHTML), -1) {
		if ids[m[1]] {
			t.Errorf("the id %q is on the page twice", m[1])
		}
		ids[m[1]] = true
	}
	used := regexp.MustCompile(`\$\('([^']+)'\)`).FindAllStringSubmatch(string(pairJS), -1)
	if len(used) < 20 {
		t.Fatalf("found only %d lookups; the pattern is stale", len(used))
	}
	for _, m := range used {
		if !ids[m[1]] {
			t.Errorf("the script uses #%s, which is not on the page", m[1])
		}
	}
	// And the routes it calls exist in the API description.
	spec := readSpec(t)
	for _, m := range regexp.MustCompile(`(?:api\('[A-Z]+', '|fetch\(')(/[a-z0-9/_-]+)`).FindAllStringSubmatch(string(pairJS), -1) {
		path := strings.TrimRight(m[1], "/")
		if !strings.Contains(spec, "  "+path+":") && !strings.Contains(spec, "  "+path+"/{") && !strings.Contains(spec, "  "+path+"/") {
			t.Errorf("the script calls %s, which api/openapi.yaml does not describe", path)
		}
	}
}

// The page speaks English and Russian from two tables (pair.en.json, pair.ru.json). They must say the same things, and
// every text the page asks for must be in them.
func pairTextTables(t *testing.T) (en, ru map[string]string) {
	t.Helper()
	if err := json.Unmarshal(pairTextsEN, &en); err != nil {
		t.Fatalf("pair.en.json: %v", err)
	}
	if err := json.Unmarshal(pairTextsRU, &ru); err != nil {
		t.Fatalf("pair.ru.json: %v", err)
	}
	return en, ru
}

var placeholderPattern = regexp.MustCompile(`\{(\w+)\}`)

func placeholdersOf(text string) string {
	var names []string
	for _, m := range placeholderPattern.FindAllStringSubmatch(text, -1) {
		names = append(names, m[1])
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestTheTwoLanguagesOfThePairingPageAgree(t *testing.T) {
	en, ru := pairTextTables(t)
	cyrillic := regexp.MustCompile(`[А-Яа-яЁё]`)
	for key, text := range en {
		other, ok := ru[key]
		if !ok {
			t.Errorf("%q is in pair.en.json but not in pair.ru.json", key)
			continue
		}
		if placeholdersOf(text) != placeholdersOf(other) {
			t.Errorf("%q has different {placeholders}: %q and %q", key, text, other)
		}
		if strings.TrimSpace(text) == "" || strings.TrimSpace(other) == "" {
			t.Errorf("%q is empty in one language", key)
		}
		if cyrillic.MatchString(text) {
			t.Errorf("%q is not English: %q", key, text)
		}
	}
	for key := range ru {
		if _, ok := en[key]; !ok {
			t.Errorf("%q is in pair.ru.json but not in pair.en.json", key)
		}
	}
}

func TestEveryTextThePairingPageUsesExistsAndIsUsed(t *testing.T) {
	en, _ := pairTextTables(t)
	html, js := string(pairHTML), string(pairJS)

	// What the page asks for: data-i18n attributes in the HTML and tr('key') in the script.
	asked := map[string]string{}
	for _, m := range regexp.MustCompile(`data-i18n(?:-label)?="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		asked[m[1]] = "pair.html"
	}
	for _, m := range regexp.MustCompile(`\btr\('([^']+)'[,)]`).FindAllStringSubmatch(js, -1) {
		asked[m[1]] = "pair.js"
	}
	for key, where := range asked {
		if _, ok := en[key]; !ok {
			t.Errorf("%s asks for the text %q, which pair.en.json does not have", where, key)
		}
	}

	// A text nobody asks for is a leftover. The ones chosen by a code from the server (err.*, skip.*) are asked for
	// by building the key, so they are checked against the codes the API documents instead.
	for key := range en {
		if strings.HasPrefix(key, "err.") || strings.HasPrefix(key, "skip.") {
			continue
		}
		if _, ok := asked[key]; !ok {
			t.Errorf("the text %q is never used", key)
		}
	}
	for _, code := range []string{"invalid_credentials", "too_many_attempts", "invalid_invite", "email_taken", "invalid_email", "weak_password", "invalid_user_code", "forbidden_origin", "invalid_request", "not_found", "conflict", "unavailable"} {
		if _, ok := en["err."+code]; !ok {
			t.Errorf("no text for the error code %q", code)
		}
	}
	for _, code := range []string{"not_decodable", "too_long"} {
		if _, ok := en["skip."+code]; !ok {
			t.Errorf("no text for the skip code %q", code)
		}
	}
}

// All the words are in the tables: neither the page nor its script carries Russian (or any) text of its own.
func TestThePairingPageAndScriptHaveNoRussianOfTheirOwn(t *testing.T) {
	if regexp.MustCompile(`[А-Яа-яЁё]`).MatchString(string(pairHTML) + string(pairJS)) {
		t.Error("pair.html or pair.js contains Cyrillic text: it belongs in pair.ru.json")
	}
}

func TestThePairingTextsAreHandedToThePageAsOneScript(t *testing.T) {
	e := newEnv(t)
	rec := e.call(http.MethodGet, "/pair.texts.js", "")
	body := rec.Body.String()
	if !strings.HasPrefix(body, "window.PAIR_TEXTS = {") || !strings.Contains(body, `"tab_login"`) || !strings.Contains(body, "Вход") || !strings.Contains(body, "Sign in") {
		t.Errorf("unexpected script: %.120q", body)
	}
	var tables map[string]map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(body, "window.PAIR_TEXTS = "), ";\n")), &tables); err != nil || len(tables["en"]) == 0 || len(tables["en"]) != len(tables["ru"]) {
		t.Errorf("the script is not the two tables: %v", err)
	}
}
