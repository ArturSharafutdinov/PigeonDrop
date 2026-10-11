package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUnits(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"cyrillic is one unit a letter", "привет", 6},
		{"emoji is two", "😀", 2},
		{"mixed", "a😀б", 4},
		{"a character outside the plane is two", "𝒳", 2},
		{"an invalid byte counts as one character", "a\xffb", 3},
	} {
		if got := Units(tc.text); got != tc.want {
			t.Errorf("%s: Units(%q) = %d, want %d", tc.name, tc.text, got, tc.want)
		}
	}
}

func TestFit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{"fits", "hello", 5, "hello"},
		{"fits with room", "hello", 10, "hello"},
		{"empty", "", 3, ""},
		{"one over", "hello!", 5, "hell…"},
		{"cyrillic", "привет, мир", 7, "привет…"},
		{"trailing space goes before the ellipsis", "ab cd", 4, "ab…"},
		{"an emoji is not split", "ab😀c", 4, "ab…"},
		{"an emoji that fits stays", "ab😀c", 5, "ab😀c"},
		{"emoji at the cut", "a😀😀", 4, "a😀…"},
		{"no room for anything", "hello", 1, "…"},
		{"no room for the ellipsis", "hello", 0, ""},
		{"an invalid byte is cut whole", "ab\xffcd", 4, "ab\xff…"},
	} {
		got := Fit(tc.text, tc.limit)
		if got != tc.want {
			t.Errorf("%s: Fit(%q, %d) = %q, want %q", tc.name, tc.text, tc.limit, got, tc.want)
		}
		if Units(got) > tc.limit {
			t.Errorf("%s: %q is %d units, over %d", tc.name, got, Units(got), tc.limit)
		}
	}
}

func TestFitNeverGoesOverAndNeverBreaksACharacter(t *testing.T) {
	text := strings.Repeat("слово 😀 word ", 400)
	for _, limit := range []int{MaxCaptionUnits, MaxMessageUnits, 1, 2, 3, 99, 1023, 1025} {
		got := Fit(text, limit)
		if Units(got) > limit {
			t.Errorf("limit %d: %d units", limit, Units(got))
		}
		if !utf8.ValidString(got) {
			t.Errorf("limit %d: the cut broke a character", limit)
		}
		if limit >= 3 && !strings.HasSuffix(got, "…") {
			t.Errorf("limit %d: %q does not say that it was cut", limit, got[max(0, len(got)-10):])
		}
	}
	if got := Fit(text[:900], MaxCaptionUnits); got != text[:900] && Units(text[:900]) <= MaxCaptionUnits {
		t.Error("a text that fits was changed")
	}
}
