package telegram

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewLinkCode(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		code, hash, err := NewLinkCode()
		if err != nil {
			t.Fatal(err)
		}
		// 128 bits as base64url: 22 characters, all of the kind Telegram allows in a /start parameter.
		if len(code) != 22 || !ValidLinkCode(code) {
			t.Fatalf("code %q", code)
		}
		if seen[code] {
			t.Fatalf("the code %q was drawn twice", code)
		}
		seen[code] = true
		if !bytes.Equal(hash, HashLinkCode(code)) || len(hash) != 32 {
			t.Fatalf("hash of %q is %x", code, hash)
		}
		if bytes.Contains(hash, []byte(code)) {
			t.Error("the hash contains the code")
		}
	}
}

func TestHashLinkCodeDiffersByCode(t *testing.T) {
	if bytes.Equal(HashLinkCode("AAAAAAAAAAAAAAAAAAAAAA"), HashLinkCode("AAAAAAAAAAAAAAAAAAAAAB")) {
		t.Error("two codes have one hash")
	}
}

func TestValidLinkCode(t *testing.T) {
	for _, ok := range []string{"AAAAAAAAAAAAAAAAAAAAAA", "abcdefghijklmnopqrstuv", "0123456789_-ABCDEFGHIJ"} {
		if !ValidLinkCode(ok) {
			t.Errorf("%q was refused", ok)
		}
	}
	for _, bad := range []string{"", "short", strings.Repeat("A", 21), strings.Repeat("A", 23), strings.Repeat("A", 64),
		"AAAAAAAAAAAAAAAAAAAAA!", "AAAAAAAAAAAAAAAAAAAAA ", " AAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAA\n", "ААААААААААААААААААААА", "AAAAAAAAAAAAAAAAAAAAA="} {
		if ValidLinkCode(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestLinkURL(t *testing.T) {
	if got := LinkURL("pigeondrop_bot", "CODE"); got != "https://t.me/pigeondrop_bot?start=CODE" {
		t.Errorf("got %q", got)
	}
}
