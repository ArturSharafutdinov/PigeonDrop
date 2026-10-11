package telegram

import (
	"strings"
	"unicode/utf8"
)

// The longest caption and the longest text message, in the units Telegram counts: UTF-16 code units, which is what
// makes one emoji count as two.
const (
	MaxCaptionUnits = 1024
	MaxMessageUnits = 4096
)

// Units is the length of s in UTF-16 code units. A byte that is not valid UTF-8 counts as the one character it is
// replaced with.
func Units(s string) int {
	n := 0
	for _, r := range s {
		n += unitsOf(r)
	}
	return n
}

func unitsOf(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// Fit returns s if it is at most limit units long, otherwise its beginning, cut between characters, with "…" in
// place of the rest. The result is never longer than limit.
func Fit(s string, limit int) string {
	if Units(s) <= limit {
		return s
	}
	const ellipsis = "…"
	budget := limit - Units(ellipsis)
	if budget < 0 {
		return ""
	}
	used, end := 0, 0
	for end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])
		if used+unitsOf(r) > budget {
			break
		}
		used += unitsOf(r)
		end += size
	}
	return strings.TrimRight(s[:end], " \t\r\n") + ellipsis
}
