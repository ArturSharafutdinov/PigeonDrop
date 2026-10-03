package auth

import (
	"errors"
	"net/mail"
	"strings"
	"unicode"
)

// ErrBadEmail is returned for text that cannot be used as an address to sign in with.
var ErrBadEmail = errors.New("not a valid email address")

// NormalizeEmail returns the form an address is stored and looked up in: trimmed and lower-cased.
// It accepts only a plain address (name@domain.tld): no display name, no spaces, a dot in the domain.
// Whether the mailbox exists is not checked; the MVP sends no mail.
func NormalizeEmail(text string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(text))
	if email == "" || len(email) > 254 || strings.ContainsFunc(email, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '"' || r == '<' || r == '>'
	}) {
		return "", ErrBadEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email {
		return "", ErrBadEmail
	}
	local, domain, _ := strings.Cut(email, "@")
	if local == "" || len(local) > 64 || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", ErrBadEmail
	}
	return email, nil
}
