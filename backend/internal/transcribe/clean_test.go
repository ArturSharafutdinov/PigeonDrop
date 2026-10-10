package transcribe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "Позвони Ивану Петровичу.", "Позвони Ивану Петровичу."},
		{"trimmed", "  \n привет \t\n", "привет"},
		{"NUL is removed", "при\x00вет", "привет"},
		{"control characters are removed", "а\x01б\x07в\x1bг\x7fд", "абвгд"},
		{"line breaks stay, in one form", "одна\r\nдве\rтри\nчетыре", "одна\nдве\nтри\nчетыре"},
		{"tabs become spaces", "а\tб", "а б"},
		{"invalid UTF-8 is replaced", "ab\xff\xfecd", "ab�cd"},
		{"empty stays empty", "", ""},
		{"only noise becomes empty", " \x00\t\x01 ", ""},
		{"Unicode is untouched", "日本語 émoji 😀 ё", "日本語 émoji 😀 ё"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanText(tt.in); got != tt.want {
				t.Errorf("CleanText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCleanTextLimitsTheLengthWithoutCuttingACharacter(t *testing.T) {
	long := strings.Repeat("ё", MaxTextRunes+500)
	got := CleanText(long)
	if n := utf8.RuneCountInString(got); n != MaxTextRunes {
		t.Errorf("length = %d runes, want %d", n, MaxTextRunes)
	}
	if !utf8.ValidString(got) {
		t.Error("the cut broke a character")
	}
	exact := strings.Repeat("a", MaxTextRunes)
	if CleanText(exact) != exact {
		t.Error("a text of exactly the limit was changed")
	}
}

func TestCleanTextIsIdempotent(t *testing.T) {
	for _, in := range []string{"a\x00b\r\nc\td", " x ", "\xff", strings.Repeat("я", MaxTextRunes+1)} {
		once := CleanText(in)
		if twice := CleanText(once); twice != once {
			t.Errorf("CleanText is not idempotent for %q", in)
		}
	}
}
