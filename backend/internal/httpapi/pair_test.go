package httpapi

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestThePairingPageIsServedWithAStrictPolicy(t *testing.T) {
	e := newEnv(t)
	for path, contentType := range map[string]string{
		"/pair":     "text/html; charset=utf-8",
		"/pair.js":  "text/javascript; charset=utf-8",
		"/pair.css": "text/css; charset=utf-8",
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
	if m := regexp.MustCompile(`(?s)<script\b[^>]*>(.*?)</script>`).FindAllStringSubmatch(html, -1); len(m) != 1 || strings.TrimSpace(m[0][1]) != "" {
		t.Errorf("the page must have exactly one script, with no inline code: %q", m)
	}
	if !strings.Contains(html, `<script src="/pair.js">`) || !strings.Contains(html, `href="/pair.css"`) {
		t.Error("the page does not load its script and style from this server")
	}
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(html) {
		t.Error("an inline event handler")
	}
	if regexp.MustCompile(`(?i)\sstyle\s*=`).MatchString(html) || strings.Contains(strings.ToLower(html), "<style") {
		t.Error("inline style")
	}
	if regexp.MustCompile(`https?://`).MatchString(html + string(pairJS) + string(pairCSS)) {
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
