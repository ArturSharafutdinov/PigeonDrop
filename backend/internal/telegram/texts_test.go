package telegram

import (
	"strings"
	"testing"
)

func TestLanguageOf(t *testing.T) {
	for code, want := range map[string]language{"ru": langRU, "RU": langRU, "ru-RU": langRU, "en": langEN, "de": langEN, "uk": langEN, "": langEN, "r": langEN} {
		if got := languageOf(code); got != want {
			t.Errorf("languageOf(%q) = %s, want %s", code, got, want)
		}
	}
}

func TestEveryPhraseIsSaidInBothLanguages(t *testing.T) {
	const page = "https://pigeon.example.com/pair"
	for _, l := range []language{langRU, langEN} {
		seen := map[string]phrase{}
		for p := phraseLinked; p <= phraseHelp; p++ {
			text := say(l, p, page)
			if strings.TrimSpace(text) == "" || strings.Contains(text, "%!") || strings.Contains(text, "%s") {
				t.Errorf("%s phrase %d is broken: %q", l, p, text)
			}
			if prev, dup := seen[text]; dup {
				t.Errorf("%s: phrases %d and %d are the same text", l, prev, p)
			}
			seen[text] = p
			if Units(text) > MaxMessageUnits {
				t.Errorf("%s phrase %d is too long for a message", l, p)
			}
		}
		// The answers that send the person to the page must say where it is.
		for _, p := range []phrase{phraseInvalidLink, phraseHowTo, phraseStopped, phraseNotLinked, phraseHelp} {
			if !strings.Contains(say(l, p, page), page) {
				t.Errorf("%s phrase %d does not name the page", l, p)
			}
		}
	}
	if !strings.Contains(say(langRU, phraseLinked, ""), "/stop") || !strings.Contains(say(langEN, phraseLinked, ""), "/stop") {
		t.Error("the confirmation does not say how to stop")
	}
	if say(langRU, phraseHelp, "x") == say(langEN, phraseHelp, "x") {
		t.Error("the two languages say the same thing")
	}
}

func TestTheMenu(t *testing.T) {
	for _, l := range []language{langRU, langEN} {
		commands := menu(l)
		if len(commands) != 2 || commands[0].Command != "stop" || commands[1].Command != "help" {
			t.Errorf("%s: %+v", l, commands)
		}
		for _, c := range commands {
			// Telegram takes 1-32 lowercase letters, digits and underscores, and a description of 3-256 characters.
			if c.Command != strings.ToLower(c.Command) || len(c.Command) > 32 || Units(c.Description) < 3 || Units(c.Description) > 256 {
				t.Errorf("%s: %+v", l, c)
			}
		}
	}
}
