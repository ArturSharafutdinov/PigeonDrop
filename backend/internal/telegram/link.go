package telegram

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"time"
)

// LinkCodeLifetime is how long the code in a link stays good.
const LinkCodeLifetime = 10 * time.Minute

// linkCodeBytes is the code's randomness: 128 bits, 22 characters once encoded, well under the 64 that Telegram
// allows in the parameter of a /start link, and made of the characters it allows (A-Z a-z 0-9 _ -).
const linkCodeBytes = 16

var linkCodeShape = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// NewLinkCode returns a fresh one-time code and the hash that is stored in its place.
func NewLinkCode() (code string, hash []byte, err error) {
	b := make([]byte, linkCodeBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("draw a link code: %w", err)
	}
	code = base64.RawURLEncoding.EncodeToString(b)
	return code, HashLinkCode(code), nil
}

// HashLinkCode is what the database keeps of a code.
func HashLinkCode(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}

// ValidLinkCode says whether a text has the shape of a code, so that nonsense is turned away without a lookup.
func ValidLinkCode(code string) bool { return linkCodeShape.MatchString(code) }

// LinkURL is the link a person opens in Telegram to link their chat: it opens the bot and, at Start, hands it the code.
func LinkURL(botUsername, code string) string {
	return "https://t.me/" + botUsername + "?start=" + code
}
