package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// Device tokens identify a watch to the server. They are long, random, and stored only as a hash, so a copy of
// the database does not let anyone upload as a watch. The "pdd_" prefix (PigeonDrop device) makes a leaked
// token recognisable to secret scanners and to a person reading a log.
const (
	DeviceTokenPrefix = "pdd_"
	// DeviceTokenLength is the length of a whole token: the prefix and 32 random bytes in unpadded base64url.
	DeviceTokenLength = len(DeviceTokenPrefix) + 43
)

// NewDeviceToken returns a fresh token for the device, and the hash to store.
func NewDeviceToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("random device token: %w", err)
	}
	token = DeviceTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return token, HashDeviceToken(token), nil
}

// HashDeviceToken is what is stored for a token (SHA-256: the token is 256 random bits, nothing to guess).
func HashDeviceToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// ParseBearer returns the device token in an Authorization header value ("Bearer pdd_..."), or ok=false when the
// header is absent or is not a well-formed device token. A token of the wrong shape is refused before any lookup.
func ParseBearer(header string) (token string, ok bool) {
	scheme, value, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if len(value) != DeviceTokenLength || !strings.HasPrefix(value, DeviceTokenPrefix) {
		return "", false
	}
	for _, r := range value[len(DeviceTokenPrefix):] {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", false
		}
	}
	return value, true
}

// Pairing a watch follows RFC 8628 (the device authorization grant): the watch asks for a pair of codes. The
// device code is a long secret that only the watch knows and polls with. The user code is short, shown on the
// watch (in a QR code and as text), and typed or scanned by the person on a second device, who approves it
// while signed in.

// NewDeviceCode returns the secret the watch polls with, and the hash to store.
func NewDeviceCode() (code string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("random device code: %w", err)
	}
	code = base64.RawURLEncoding.EncodeToString(raw)
	return code, HashDeviceCode(code), nil
}

// DeviceCodeLength is the length of a device code as the watch holds it.
const DeviceCodeLength = 43

func HashDeviceCode(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}

// User codes use RFC 8628's suggested alphabet: twenty consonants, so that no code spells a word and none is
// ambiguous when read aloud or typed (no vowels, no 0/O or 1/I). Eight characters are 34 bits; that is only
// safe because a code lives minutes and wrong guesses are limited.
const (
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLength   = 8
)

// NewUserCode returns a fresh code in the form shown to people (BCDF-GHJK) and in the canonical form that is stored.
func NewUserCode() (display, canonical string, err error) {
	chars := make([]byte, 0, userCodeLength)
	buf := make([]byte, 16)
	for len(chars) < userCodeLength {
		if _, err := rand.Read(buf); err != nil {
			return "", "", fmt.Errorf("random user code: %w", err)
		}
		for _, b := range buf {
			// 240 is the largest multiple of 20 below 256: bytes above it are skipped so every letter is equally likely.
			if b < 240 && len(chars) < userCodeLength {
				chars = append(chars, userCodeAlphabet[int(b)%len(userCodeAlphabet)])
			}
		}
	}
	canonical = string(chars)
	return DisplayUserCode(canonical), canonical, nil
}

// DisplayUserCode writes a canonical code the way people see it: two groups of four.
func DisplayUserCode(canonical string) string {
	if len(canonical) != userCodeLength {
		return canonical
	}
	return canonical[:4] + "-" + canonical[4:]
}

// ParseUserCode turns what a person typed into the canonical code. Case, dashes and spaces do not matter;
// anything else that is not one of the twenty letters, or the wrong length, is refused.
func ParseUserCode(text string) (canonical string, ok bool) {
	out := make([]byte, 0, userCodeLength)
	for _, r := range strings.ToUpper(text) {
		switch {
		case r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r':
			continue
		case r > 127 || strings.IndexByte(userCodeAlphabet, byte(r)) < 0 || len(out) == userCodeLength:
			return "", false
		}
		out = append(out, byte(r))
	}
	if len(out) != userCodeLength {
		return "", false
	}
	return string(out), true
}
