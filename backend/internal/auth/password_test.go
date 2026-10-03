package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Hashes made by Node's crypto.argon2Sync (OpenSSL), an Argon2 implementation independent of
// golang.org/x/crypto, with salt "0123456789abcdef". They check the PHC format and the parameters,
// not just that Hash and Verify agree with each other.
const (
	nodeHash  = "$argon2id$v=19$m=19456,t=2,p=1$MDEyMzQ1Njc4OWFiY2RlZg$gy5SuVm5Z7Vw7keB9se9p87QGcomaseB/S2U1OhTsM0"
	nodeHash2 = "$argon2id$v=19$m=64,t=3,p=4$MDEyMzQ1Njc4OWFiY2RlZg$EG06xVquOLREzb691L6vVY55BUQDFk6GoG5BevCbiSU"
)

func fastHasher() *Hasher { return NewHasher(Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1}, 2) }

func TestVerifyAcceptsHashesFromAnIndependentImplementation(t *testing.T) {
	h := NewHasher(DefaultParams, 2)
	ctx := context.Background()

	ok, rehash, err := h.Verify(ctx, nodeHash, "correct horse battery staple")
	if err != nil || !ok || rehash {
		t.Fatalf("default-parameter hash: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, _ := h.Verify(ctx, nodeHash, "correct horse battery stapl"); ok {
		t.Fatal("a wrong password was accepted")
	}

	// Other parameters verify too, and are flagged for an upgrade.
	ok, rehash, err = h.Verify(ctx, nodeHash2, "pässwörd with üñicode")
	if err != nil || !ok || !rehash {
		t.Fatalf("other-parameter hash: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
}

func TestHashRoundTripAndFormat(t *testing.T) {
	h := fastHasher()
	ctx := context.Background()

	a, err := h.Hash(ctx, "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := h.Hash(ctx, "a long enough password")
	if a == b {
		t.Fatal("two hashes of one password are identical: the salt is not random")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("unexpected format: %s", a)
	}
	if ok, rehash, err := h.Verify(ctx, a, "a long enough password"); err != nil || !ok || rehash {
		t.Fatalf("round trip: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	for _, wrong := range []string{"", "a long enough passwor", "A long enough password", "a long enough password "} {
		if ok, _, _ := h.Verify(ctx, a, wrong); ok {
			t.Fatalf("accepted %q", wrong)
		}
	}
}

func TestPasswordsAreCompareUnderUnicodeNormalization(t *testing.T) {
	h := fastHasher()
	ctx := context.Background()
	composed := "café au lait, s'il vous plait"
	decomposed := "café au lait, s'il vous plait"

	hash, _ := h.Hash(ctx, composed)
	if ok, _, _ := h.Verify(ctx, hash, decomposed); !ok {
		t.Fatal("the same password typed with a decomposed é was refused")
	}
}

func TestHashSettingsChangeIsDetected(t *testing.T) {
	ctx := context.Background()
	old := fastHasher()
	hash, _ := old.Hash(ctx, "a long enough password")

	for name, p := range map[string]Params{
		"memory":     {MemoryKiB: 128, Iterations: 1, Parallelism: 1},
		"iterations": {MemoryKiB: 64, Iterations: 2, Parallelism: 1},
		"lanes":      {MemoryKiB: 64, Iterations: 1, Parallelism: 2},
	} {
		ok, rehash, err := NewHasher(p, 1).Verify(ctx, hash, "a long enough password")
		if err != nil || !ok || !rehash {
			t.Errorf("%s: ok=%v rehash=%v err=%v", name, ok, rehash, err)
		}
	}
}

func TestMalformedHashesAreErrorsNotPanics(t *testing.T) {
	h := fastHasher()
	good, _ := h.Hash(context.Background(), "a long enough password")
	parts := strings.Split(good, "$")
	with := func(i int, v string) string {
		p := append([]string(nil), parts...)
		p[i] = v
		return strings.Join(p, "$")
	}

	for name, bad := range map[string]string{
		"empty":             "",
		"plain text":        "hunter2",
		"too few parts":     "$argon2id$v=19$m=64,t=1,p=1$c2FsdHNhbHQ",
		"too many parts":    good + "$x",
		"argon2i":           with(1, "argon2i"),
		"old version":       with(2, "v=16"),
		"missing params":    with(3, "m=64,t=1"),
		"misordered params": with(3, "t=1,m=64,p=1"),
		"non-numeric":       with(3, "m=lots,t=1,p=1"),
		"zero iterations":   with(3, "m=64,t=0,p=1"),
		"huge memory":       with(3, "m=4194304,t=1,p=1"),
		"huge iterations":   with(3, "m=64,t=1000000,p=1"),
		"too many lanes":    with(3, "m=64,t=1,p=255"),
		"tiny memory":       with(3, "m=1,t=1,p=1"),
		"bad salt":          with(4, "!!!"),
		"short salt":        with(4, "AAAA"),
		"bad key":           with(5, "***"),
		"short key":         with(5, "AAAA"),
		"padded base64":     with(5, parts[5]+"="),
	} {
		ok, _, err := h.Verify(context.Background(), bad, "a long enough password")
		if ok || err == nil {
			t.Errorf("%s: ok=%v err=%v", name, ok, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		name     string
		password string
		want     error
	}{
		{"empty", "", ErrPasswordTooShort},
		{"eleven characters", strings.Repeat("a", 11), ErrPasswordTooShort},
		{"twelve characters", strings.Repeat("a", 12), nil},
		// Length counts characters, not bytes: twelve emoji are 48 bytes but twelve characters.
		{"twelve multi-byte characters", strings.Repeat("🔑", 12), nil},
		{"eleven multi-byte characters", strings.Repeat("🔑", 11), ErrPasswordTooShort},
		{"256 bytes", strings.Repeat("a", 256), nil},
		{"257 bytes", strings.Repeat("a", 257), ErrPasswordTooLong},
		// NFKC turns the fullwidth letters into plain ones (same length), and the ligature "ﬃ" into three.
		{"compatibility characters", strings.Repeat("ａ", 12), nil},
	}
	for _, c := range cases {
		if err := ValidatePassword(c.password); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestHashingWaitsForASlotAndGivesUpWithItsContext(t *testing.T) {
	h := fastHasher()
	// Initialise the dummy hash first: it is made once, and must not wait on the slots the test fills.
	if err := h.VerifyUnknown(context.Background(), "whatever"); err != nil {
		t.Fatal(err)
	}
	for range cap(h.slots) {
		h.slots <- struct{}{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "a long enough password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Hash with every slot taken: %v", err)
	}
	// Every path that hashes goes through the slots: an unknown account costs what a known one does.
	if err := h.VerifyUnknown(ctx, "whatever"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("VerifyUnknown with every slot taken: %v", err)
	}
	good, _ := fastHasher().Hash(context.Background(), "a long enough password")
	if _, _, err := h.Verify(ctx, good, "a long enough password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Verify with every slot taken: %v", err)
	}

	for range cap(h.slots) {
		<-h.slots
	}
	if _, err := h.Hash(context.Background(), "a long enough password"); err != nil {
		t.Fatalf("Hash after the slots were freed: %v", err)
	}
}

func TestSlotsAreReleasedAfterEveryOutcome(t *testing.T) {
	h := NewHasher(Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1}, 1)
	ctx := context.Background()
	hash, _ := h.Hash(ctx, "a long enough password")
	// With a single slot, a leak on any path would make the next call hang.
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Verify(ctx, hash, "a long enough password")
		h.Verify(ctx, hash, "wrong wrong wrong")
		h.Verify(ctx, "garbage", "x")
		h.VerifyUnknown(ctx, "x")
		h.Hash(ctx, "a long enough password")
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a slot was never released")
	}
}
