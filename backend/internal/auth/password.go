// Package auth holds the pieces of sign-in that need no database or HTTP: password hashing, random
// tokens, invitation codes, input checks and the brute-force limiter.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

// Params are the Argon2id cost settings.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
}

// DefaultParams is the OWASP Password Storage Cheat Sheet's lowest recommended Argon2id setting
// (19 MiB, 2 passes, 1 lane). The memory is small on purpose: the server is a shared VPS, and
// Hasher caps how many hashes run at once.
var DefaultParams = Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1}

const (
	saltLen = 16
	keyLen  = 32

	// Limits on what Verify accepts from a stored hash, so that a damaged or hostile row cannot make it
	// allocate gigabytes.
	maxMemoryKiB   = 512 * 1024
	maxIterations  = 20
	maxParallelism = 16
)

// Password length rules. NIST SP 800-63B asks for at least 15 characters when a password is the only
// factor and allows at least 64; the upper limit here keeps a request from making the hash slower.
const (
	MinPasswordRunes = 12
	MaxPasswordBytes = 256
	// MaxPasswordInputBytes is the most a password may be as it arrives, before it is normalized (it is the
	// maxLength of the password fields in api/openapi.yaml, which counts bytes for this purpose). NFKC can make text
	// shorter, by 4 to 1 at the most (a mathematical letter of four bytes becomes an ASCII letter of one), so every
	// password that is within MaxPasswordBytes once normalized is within four times that before; nothing larger
	// can be right, and it is refused before any work is spent on it. Without this a request body of megabytes
	// (the server reads up to 8 MiB of any request) would be normalized and hashed.
	MaxPasswordInputBytes = 4 * MaxPasswordBytes
)

var (
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordRunes)
	ErrPasswordTooLong  = fmt.Errorf("password must be at most %d bytes", MaxPasswordBytes)
)

// InputSizeOK reports whether password is small enough to be looked at: checked, or turned into a hash. Every
// request that carries a password asks this before it does anything with it (ValidatePassword does so itself);
// when it says no, the password cannot be one that was ever accepted, so nobody is told more than "too long".
func InputSizeOK(password string) bool { return len(password) <= MaxPasswordInputBytes }

// ValidatePassword applies the length rules to the password as it will be hashed.
func ValidatePassword(password string) error {
	if !InputSizeOK(password) {
		return ErrPasswordTooLong // before normalizing: that costs time and memory in proportion to the size
	}
	password = normalizePassword(password)
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}
	if utf8.RuneCountInString(password) < MinPasswordRunes {
		return ErrPasswordTooShort
	}
	return nil
}

// normalizePassword applies Unicode NFKC, as NIST recommends, so a password typed on another keyboard
// or device (a composed or a decomposed "é") is still the same password.
func normalizePassword(password string) string {
	return norm.NFKC.String(password)
}

// Hasher hashes and verifies passwords with Argon2id. At most a few hashes run at once: each one holds
// its memory for tens of milliseconds, and a burst of logins must not take the whole server down.
type Hasher struct {
	params Params
	slots  chan struct{}

	dummyOnce sync.Once
	dummy     string
}

// NewHasher returns a Hasher that creates hashes with params and runs at most maxConcurrent at a time.
func NewHasher(params Params, maxConcurrent int) *Hasher {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Hasher{params: params, slots: make(chan struct{}, maxConcurrent)}
}

// acquire waits for a free slot, or for ctx to end. The returned function frees the slot.
func (h *Hasher) acquire(ctx context.Context) (func(), error) {
	select {
	case h.slots <- struct{}{}:
		return func() { <-h.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Hash returns the PHC string for password: $argon2id$v=19$m=...,t=...,p=...$salt$hash.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if !InputSizeOK(password) {
		return "", ErrPasswordTooLong
	}
	release, err := h.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("random salt: %w", err)
	}
	key := argon2.IDKey([]byte(normalizePassword(password)), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, keyLen)
	return encode(h.params, salt, key), nil
}

// Verify reports whether password matches the stored hash. needsRehash is true when it does, but the
// hash was made with other settings than this Hasher's current ones: the caller should store a new one.
func (h *Hasher) Verify(ctx context.Context, encoded, password string) (ok, needsRehash bool, err error) {
	if !InputSizeOK(password) {
		return false, false, ErrPasswordTooLong
	}
	params, salt, want, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	release, err := h.acquire(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()

	got := argon2.IDKey([]byte(normalizePassword(password)), salt, params.Iterations, params.MemoryKiB, params.Parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	return true, params != h.params, nil
}

// VerifyUnknown spends the time of a real verification and reports nothing. Sign-in calls it when the
// email is not registered, so the answer does not come back faster than for a registered one.
func (h *Hasher) VerifyUnknown(ctx context.Context, password string) error {
	h.dummyOnce.Do(func() {
		random := make([]byte, 16)
		_, _ = rand.Read(random)
		h.dummy, _ = h.Hash(context.Background(), base64.RawStdEncoding.EncodeToString(random))
	})
	_, _, err := h.Verify(ctx, h.dummy, password)
	return err
}

func encode(p Params, salt, key []byte) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism, b64.EncodeToString(salt), b64.EncodeToString(key))
}

var errBadHash = errors.New("malformed password hash")

func decode(encoded string) (p Params, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=...,t=...,p=...", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, errBadHash
	}
	var m, t, par uint64
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return Params{}, nil, nil, errBadHash
	}
	for i, f := range fields {
		name, value, found := strings.Cut(f, "=")
		if !found || name != string("mtp"[i]) {
			return Params{}, nil, nil, errBadHash
		}
		n, perr := strconv.ParseUint(value, 10, 32)
		if perr != nil {
			return Params{}, nil, nil, errBadHash
		}
		switch i {
		case 0:
			m = n
		case 1:
			t = n
		case 2:
			par = n
		}
	}
	if m < 8 || m > maxMemoryKiB || t < 1 || t > maxIterations || par < 1 || par > maxParallelism {
		return Params{}, nil, nil, errBadHash
	}
	b64 := base64.RawStdEncoding
	if salt, err = b64.DecodeString(parts[4]); err != nil || len(salt) < 8 || len(salt) > 64 {
		return Params{}, nil, nil, errBadHash
	}
	if key, err = b64.DecodeString(parts[5]); err != nil || len(key) < 16 || len(key) > 64 {
		return Params{}, nil, nil, errBadHash
	}
	return Params{MemoryKiB: uint32(m), Iterations: uint32(t), Parallelism: uint8(par)}, salt, key, nil
}
