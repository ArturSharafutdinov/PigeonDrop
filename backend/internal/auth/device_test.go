package auth

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestDeviceTokens(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		token, hash, err := NewDeviceToken()
		if err != nil {
			t.Fatal(err)
		}
		if len(token) != DeviceTokenLength || !strings.HasPrefix(token, "pdd_") || !regexp.MustCompile(`^pdd_[A-Za-z0-9_-]{43}$`).MatchString(token) {
			t.Fatalf("token %q has the wrong shape", token)
		}
		if len(hash) != 32 || !bytes.Equal(hash, HashDeviceToken(token)) || bytes.Contains(hash, []byte(token)) {
			t.Fatal("the stored hash is wrong")
		}
		if seen[token] {
			t.Fatal("token repeated")
		}
		seen[token] = true

		// What the watch sends comes back out of the header unchanged.
		if got, ok := ParseBearer("Bearer " + token); !ok || got != token {
			t.Fatalf("ParseBearer lost the token: %q %v", got, ok)
		}
	}
}

func TestParseBearer(t *testing.T) {
	token, _, _ := NewDeviceToken()
	good := map[string]string{
		"Bearer " + token:        token,
		"bearer " + token:        token,
		"BEARER  " + token + " ": token,
		"  Bearer " + token:      token,
	}
	for header, want := range good {
		if got, ok := ParseBearer(header); !ok || got != want {
			t.Errorf("%q: %q %v", header, got, ok)
		}
	}
	for name, header := range map[string]string{
		"empty":           "",
		"no scheme":       token,
		"basic":           "Basic " + token,
		"no token":        "Bearer",
		"no token, space": "Bearer ",
		"short":           "Bearer " + token[:len(token)-1],
		"long":            "Bearer " + token + "A",
		"wrong prefix":    "Bearer xxx_" + token[4:],
		"bad character":   "Bearer " + token[:10] + "!" + token[11:],
		"space inside":    "Bearer " + token[:10] + " " + token[11:],
		"a session token": "Bearer " + strings.Repeat("A", SessionTokenLength),
	} {
		if got, ok := ParseBearer(header); ok {
			t.Errorf("%s: accepted %q as %q", name, header, got)
		}
	}
}

func TestDeviceCodes(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		code, hash, err := NewDeviceCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != DeviceCodeLength || !bytes.Equal(hash, HashDeviceCode(code)) || seen[code] {
			t.Fatalf("code %q", code)
		}
		seen[code] = true
	}
}

var userCodeShape = regexp.MustCompile(`^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`)

func TestUserCodes(t *testing.T) {
	counts := map[rune]int{}
	seen := map[string]bool{}
	const n = 5000
	for range n {
		display, canonical, err := NewUserCode()
		if err != nil {
			t.Fatal(err)
		}
		if !userCodeShape.MatchString(display) || strings.ReplaceAll(display, "-", "") != canonical || len(canonical) != 8 {
			t.Fatalf("code %q / %q", display, canonical)
		}
		if got, ok := ParseUserCode(display); !ok || got != canonical {
			t.Fatalf("%q does not parse back to %q", display, canonical)
		}
		seen[canonical] = true
		for _, r := range canonical {
			counts[r]++
		}
	}
	if len(counts) != 20 {
		t.Fatalf("%d of 20 letters ever appear", len(counts))
	}
	// 40 000 letters over 20 symbols: about 2000 each. A modulo bias or a skipped letter would stand out
	// far beyond this tolerance (standard deviation is about 44).
	for r, c := range counts {
		if c < 1700 || c > 2300 {
			t.Errorf("letter %c appeared %d times of %d, expected about 2000", r, c, n*8)
		}
	}
	if len(seen) < n-5 {
		t.Errorf("%d distinct codes in %d draws", len(seen), n)
	}
}

func TestParseUserCode(t *testing.T) {
	for typed, want := range map[string]string{
		"BCDF-GHJK":       "BCDFGHJK",
		"bcdf-ghjk":       "BCDFGHJK",
		"BCDFGHJK":        "BCDFGHJK",
		" bcdf ghjk\n":    "BCDFGHJK",
		"b-c-d-f-g-h-j-k": "BCDFGHJK",
	} {
		if got, ok := ParseUserCode(typed); !ok || got != want {
			t.Errorf("%q: %q %v", typed, got, ok)
		}
	}
	for name, typed := range map[string]string{
		"empty":     "",
		"short":     "BCDF-GHJ",
		"long":      "BCDF-GHJKL",
		"vowel":     "BCDF-GHJA",
		"digit":     "BCDF-GHJ0",
		"punct":     "BCDF-GHJ!",
		"non-ASCII": "BCDF-GHJЖ",
		"two codes": "BCDF-GHJK BCDF-GHJK",
		"nul":       "BCDF\x00GHJK",
	} {
		if got, ok := ParseUserCode(typed); ok {
			t.Errorf("%s: accepted %q as %q", name, typed, got)
		}
	}
}

func TestDisplayUserCodeLeavesOtherLengthsAlone(t *testing.T) {
	if DisplayUserCode("BCDFGHJK") != "BCDF-GHJK" || DisplayUserCode("BCD") != "BCD" {
		t.Fatal("DisplayUserCode")
	}
}

func FuzzParseUserCode(f *testing.F) {
	for _, s := range []string{"", "BCDF-GHJK", "bcdfghjk", "\xff", strings.Repeat("-", 4000), "BCDFGHJKBCDFGHJK"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		code, ok := ParseUserCode(text)
		if !ok {
			if code != "" {
				t.Fatal("code returned with ok=false")
			}
			return
		}
		if !regexp.MustCompile(`^[BCDFGHJKLMNPQRSTVWXZ]{8}$`).MatchString(code) {
			t.Fatalf("%q parsed to %q", text, code)
		}
		if again, ok := ParseUserCode(DisplayUserCode(code)); !ok || again != code {
			t.Fatalf("%q is not stable", code)
		}
	})
}

func FuzzParseBearer(f *testing.F) {
	token, _, _ := NewDeviceToken()
	for _, s := range []string{"", "Bearer " + token, "bearer", "Bearer  ", "\x00", strings.Repeat("Bearer ", 500)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, header string) {
		got, ok := ParseBearer(header)
		if ok && (len(got) != DeviceTokenLength || !strings.HasPrefix(got, DeviceTokenPrefix)) {
			t.Fatalf("%q yielded %q", header, got)
		}
	})
}
