package auth

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestSessionTokens(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		token, hash, err := NewSessionToken()
		if err != nil {
			t.Fatal(err)
		}
		if len(token) != SessionTokenLength || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(token) {
			t.Fatalf("token %q is not %d characters of base64url", token, SessionTokenLength)
		}
		if len(hash) != 32 || !bytes.Equal(hash, HashSessionToken(token)) {
			t.Fatalf("stored hash does not match the token's hash")
		}
		if bytes.Contains(hash, []byte(token)) || seen[token] {
			t.Fatal("token repeated or stored in the clear")
		}
		seen[token] = true
	}
	if bytes.Equal(HashSessionToken("a"), HashSessionToken("b")) {
		t.Fatal("different tokens hash alike")
	}
}

var inviteFormat = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){4}$`)

func TestInviteCodes(t *testing.T) {
	seen := map[string]bool{}
	symbols := map[rune]bool{}
	for range 2000 {
		code, hash, err := NewInviteCode()
		if err != nil {
			t.Fatal(err)
		}
		if !inviteFormat.MatchString(code) {
			t.Fatalf("code %q has the wrong shape", code)
		}
		parsed, ok := ParseInviteCode(code)
		if !ok || !bytes.Equal(parsed, hash) || len(hash) != 32 {
			t.Fatalf("code %q does not parse back to its stored hash", code)
		}
		if seen[code] {
			t.Fatalf("code %q repeated", code)
		}
		seen[code] = true
		for _, r := range code {
			symbols[r] = true
		}
	}
	// 2000 codes x 20 characters: a symbol that never appears means the alphabet is not all in use.
	if len(symbols) != len(inviteAlphabet)+1 { // + the dash
		t.Fatalf("only %d distinct symbols appeared", len(symbols))
	}
}

func TestParseInviteCodeForgivesTyping(t *testing.T) {
	code, want, _ := NewInviteCode()
	compact := strings.ReplaceAll(code, "-", "")

	for name, typed := range map[string]string{
		"as shown":          code,
		"lower case":        strings.ToLower(code),
		"no dashes":         compact,
		"spaces for dashes": strings.ReplaceAll(code, "-", " "),
		"padded":            "  " + code + "\n",
		"mixed":             strings.ToLower(compact[:7]) + "-" + compact[7:],
	} {
		if got, ok := ParseInviteCode(typed); !ok || !bytes.Equal(got, want) {
			t.Errorf("%s: %q was not read as the code (ok=%v)", name, typed, ok)
		}
	}

	// O, I and L are not in the alphabet; they are read as 0, 1, 1.
	a, okA := ParseInviteCode("0000-1111-1111-OOOO-0O0O")
	b, okB := ParseInviteCode("OOOO-IIII-LLLL-0000-0000")
	if !okA || !okB || !bytes.Equal(a, b) {
		t.Fatal("O/0 and I/L/1 are not treated alike")
	}
}

func TestParseInviteCodeRefusesWhatIsNotACode(t *testing.T) {
	code, _, _ := NewInviteCode()
	compact := strings.ReplaceAll(code, "-", "")

	for name, text := range map[string]string{
		"empty":          "",
		"blank":          "   ",
		"too short":      compact[:19],
		"too long":       compact + "0",
		"far too long":   strings.Repeat("A", 10_000),
		"letter U":       "U" + compact[1:],
		"punctuation":    compact[:10] + "!" + compact[11:],
		"non-ASCII":      "é" + compact[1:],
		"two codes":      code + "-" + code,
		"null byte":      compact[:5] + "\x00" + compact[6:],
		"other alphabet": strings.Repeat("я", 20),
	} {
		if h, ok := ParseInviteCode(text); ok || h != nil {
			t.Errorf("%s: accepted %q", name, text)
		}
	}
}

func FuzzParseInviteCode(f *testing.F) {
	code, _, _ := NewInviteCode()
	for _, seed := range []string{"", code, strings.ToLower(code), "OIL0-1111-0000-0000-0000", "\xff\xfe", strings.Repeat("-", 5000)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		hash, ok := ParseInviteCode(text)
		if !ok {
			if hash != nil {
				t.Fatal("hash returned for a refused code")
			}
			return
		}
		if len(hash) != 32 {
			t.Fatalf("hash length %d", len(hash))
		}
		// Case and dashes never matter.
		again, ok2 := ParseInviteCode(strings.ToLower(strings.ReplaceAll(text, "-", "")))
		if !ok2 || !bytes.Equal(hash, again) {
			t.Fatalf("%q reads differently once lower-cased and without dashes", text)
		}
	})
}
