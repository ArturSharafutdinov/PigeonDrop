package auth

import (
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	good := map[string]string{
		"alice@example.com":              "alice@example.com",
		"  Alice@Example.COM \n":         "alice@example.com",
		"a.b+tag@sub.example.co.uk":      "a.b+tag@sub.example.co.uk",
		"user_name-1@example-host.org":   "user_name-1@example-host.org",
		"почта@пример.рф":                "почта@пример.рф",
		strings.Repeat("a", 64) + "@x.y": strings.Repeat("a", 64) + "@x.y",
	}
	for in, want := range good {
		if got, err := NormalizeEmail(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}

	bad := []string{
		"", "   ", "alice", "alice@", "@example.com", "alice@example", "alice@.com", "alice@example.",
		"alice@@example.com", "alice example@example.com", "alice@exam ple.com",
		"Alice <alice@example.com>", "<alice@example.com>", `"alice"@example.com`,
		"alice@example.com, bob@example.com", "alice@example.com\nBcc: x@y.z",
		strings.Repeat("a", 65) + "@example.com",  // local part over 64
		strings.Repeat("a", 250) + "@example.com", // over 254 in all
		"alice@example.com\x00", "al\tice@example.com",
	}
	for _, in := range bad {
		if got, err := NormalizeEmail(in); err == nil {
			t.Errorf("%q was accepted as %q", in, got)
		}
	}
}

func FuzzNormalizeEmail(f *testing.F) {
	for _, s := range []string{"a@b.c", "A@B.C ", "", "x@y", "a b@c.d", "\xff@x.y", "a@b.c\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := NormalizeEmail(in)
		if err != nil {
			return
		}
		again, err := NormalizeEmail(out)
		if err != nil || again != out {
			t.Fatalf("%q -> %q is not stable: %q, %v", in, out, again, err)
		}
		if out != strings.ToLower(out) || len(out) > 254 || strings.ContainsAny(out, " \t\r\n\"<>,") {
			t.Fatalf("%q -> %q breaks the stored form", in, out)
		}
	})
}
