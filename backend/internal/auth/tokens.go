package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode"
)

// SessionTokenLength is the length of a session token as it appears in the cookie.
const SessionTokenLength = 43 // 32 random bytes in unpadded base64url

// NewSessionToken returns a fresh random token for the cookie and the hash to store: the database
// never holds a token that could be used to sign in.
func NewSessionToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("random session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashSessionToken(token), nil
}

// HashSessionToken is what is stored for a token. A fast hash is right here: the token is 256 random
// bits, so there is nothing to guess and no reason to slow the lookup.
func HashSessionToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Invitation codes are 20 characters of Crockford's base32 (no I, L, O, U), 100 random bits, written in
// groups of four: 7K3M-Q9XD-4TNA-B2WH-C8RY.
const (
	inviteAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	inviteLength   = 20
)

// NewInviteCode returns a new code to show once, and the hash to store.
func NewInviteCode() (code string, hash []byte, err error) {
	raw := make([]byte, inviteLength)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("random invitation code: %w", err)
	}
	var sb strings.Builder
	for i, b := range raw {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(inviteAlphabet[b&31]) // 32 divides 256, so every character is equally likely
	}
	code = sb.String()
	hash, _ = ParseInviteCode(code)
	return code, hash, nil
}

// ParseInviteCode turns what a person typed into the hash to look up. It ignores case, dashes and
// whitespace, and reads O as 0 and I or L as 1, as Crockford's alphabet does for mistyped characters.
// ok is false when the text cannot be a code at all.
func ParseInviteCode(text string) (hash []byte, ok bool) {
	canonical := make([]byte, 0, inviteLength)
	for _, r := range strings.ToUpper(text) {
		switch {
		case r == '-' || unicode.IsSpace(r):
			continue
		case r == 'O':
			r = '0'
		case r == 'I' || r == 'L':
			r = '1'
		}
		if r > 127 || strings.IndexByte(inviteAlphabet, byte(r)) < 0 || len(canonical) == inviteLength {
			return nil, false
		}
		canonical = append(canonical, byte(r))
	}
	if len(canonical) != inviteLength {
		return nil, false
	}
	sum := sha256.Sum256(canonical)
	return sum[:], true
}
