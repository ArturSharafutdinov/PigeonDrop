package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
)

// The "Personal server" tab of the pairing page hands a watch the address and the access token of a server the
// person runs themselves. Those two values are encrypted in the browser with a key only the watch has (it is in
// the QR code after the "#", which a browser never sends), so what this file handles is an envelope the server
// cannot read. It still treats the envelope as something to keep as little as possible:
//
//   - it lives in this process's memory and nowhere else: not in the database, not in a log, not in a backup;
//   - for at most as long as the pairing it belongs to, and for one delivery: the watch's next poll takes it out;
//   - nothing prints it: the type redacts itself in every formatting verb and in structured logs.
//
// If two containers run during a deployment a request may land in one and the poll in the other; the pairing then
// runs out and the person starts again. That is accepted (docs/personal-server.md).

const (
	// relayVersion is the only envelope format there is.
	relayVersion = 1
	// relayBodyLimit is the largest request body of /v1/device/relay: an envelope is at most about 1.5 KiB.
	relayBodyLimit = 4 << 10
	// maxRelayEntries bounds the memory a flood can take: pairings that are open at the same time are far fewer.
	maxRelayEntries = 1000

	relayMinCipherBytes = 32 // the empty first block, and at least one block of the JSON
	relayMaxCipherBytes = 1024
	relayTagBytes       = 32 // HMAC-SHA-256
)

// relayEnvelope is what waits for the watch. The fields are unexported, so that encoding it by accident gives {}.
type relayEnvelope struct{ ct, tag string }

func (relayEnvelope) String() string               { return "relayEnvelope(redacted)" }
func (relayEnvelope) GoString() string             { return "relayEnvelope(redacted)" }
func (relayEnvelope) LogValue() slog.Value         { return slog.StringValue("relayEnvelope(redacted)") }
func (e relayEnvelope) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(e.String())) }
func (e relayEnvelope) toAPI() *api.RelayEnvelope {
	return &api.RelayEnvelope{V: relayVersion, Ct: e.ct, Tag: e.tag}
}

type relayEntry struct {
	envelope relayEnvelope
	expires  time.Time
	timer    *time.Timer
}

// A struct's unexported fields are printed without asking their types, so an entry hides its envelope itself.
func (relayEntry) String() string   { return "relayEntry(redacted)" }
func (relayEntry) GoString() string { return "relayEntry(redacted)" }

// relayStore holds the envelopes, keyed by the id of the pairing they are for. Safe for concurrent use.
type relayStore struct {
	max int
	now func() time.Time

	mu      sync.Mutex
	entries map[uuid.UUID]*relayEntry
}

func newRelayStore(max int, now func() time.Time) *relayStore {
	return &relayStore{max: max, now: now, entries: make(map[uuid.UUID]*relayEntry)}
}

// put stores the envelope until expires, replacing one that waits for the same pairing (the last sender wins until
// the watch has collected). When the store is full it refuses, and says how long until room is likely.
func (r *relayStore) put(id uuid.UUID, e relayEnvelope, expires time.Time) (wait time.Duration, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	ttl := expires.Sub(now)
	if ttl <= 0 {
		return 0, true // the pairing is over; nothing to keep
	}
	r.purgeLocked(now)
	old := r.entries[id]
	if old == nil && len(r.entries) >= r.max {
		return r.soonestLocked(now), false
	}
	if old != nil {
		old.timer.Stop()
	}
	entry := &relayEntry{envelope: e, expires: expires}
	// Even if nobody asks for a long time, the envelope leaves memory when its time is up.
	entry.timer = time.AfterFunc(ttl, func() { r.expire(id, entry) })
	r.entries[id] = entry
	return 0, true
}

// has says whether an envelope is waiting for the pairing, without taking it.
func (r *relayStore) has(id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[id]
	return entry != nil && entry.expires.After(r.now())
}

// take hands out the envelope and forgets it. Exactly one caller gets it.
func (r *relayStore) take(id uuid.UUID) (relayEnvelope, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[id]
	if entry == nil {
		return relayEnvelope{}, false
	}
	entry.timer.Stop()
	delete(r.entries, id)
	if !entry.expires.After(r.now()) {
		return relayEnvelope{}, false
	}
	return entry.envelope, true
}

// size is the number of envelopes in memory, expired ones not counted (and dropped).
func (r *relayStore) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purgeLocked(r.now())
	return len(r.entries)
}

func (r *relayStore) expire(id uuid.UUID, entry *relayEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[id] == entry {
		delete(r.entries, id)
	}
}

func (r *relayStore) purgeLocked(now time.Time) {
	for id, entry := range r.entries {
		if !entry.expires.After(now) {
			entry.timer.Stop()
			delete(r.entries, id)
		}
	}
}

func (r *relayStore) soonestLocked(now time.Time) time.Duration {
	wait := time.Duration(0)
	for _, entry := range r.entries {
		if d := entry.expires.Sub(now); wait == 0 || d < wait {
			wait = d
		}
	}
	return max(wait, time.Second)
}

// validRelayEnvelope checks the shape of an envelope, which is all the server can check: it has no key. The
// content is the watch's to verify.
func validRelayEnvelope(e api.RelayEnvelope) bool {
	enc := base64.RawURLEncoding.Strict()
	ct, err := enc.DecodeString(e.Ct)
	if err != nil || len(ct)%16 != 0 || len(ct) < relayMinCipherBytes || len(ct) > relayMaxCipherBytes {
		return false
	}
	tag, err := enc.DecodeString(e.Tag)
	return err == nil && len(tag) == relayTagBytes
}

// RelayDeviceSettings keeps an encrypted envelope for the watch that shows the code, until its next poll.
func (s *Server) RelayDeviceSettings(ctx context.Context, request api.RelayDeviceSettingsRequestObject) (api.RelayDeviceSettingsResponseObject, error) {
	client := clientFrom(ctx)
	if wait, blocked := s.Limits.DeviceRelayIP.Blocked(client); blocked {
		return api.RelayDeviceSettings429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}
	body := request.Body
	if body == nil {
		return api.RelayDeviceSettings400JSONResponse(coded(api.ErrorCodeInvalidRequest, "expected a JSON body")), nil
	}
	// The messages below never repeat anything from the body.
	if body.Envelope.V != relayVersion {
		return api.RelayDeviceSettings400JSONResponse(coded(api.ErrorCodeUnsupportedVersion, "unsupported envelope version")), nil
	}
	if !validRelayEnvelope(body.Envelope) {
		return api.RelayDeviceSettings400JSONResponse(coded(api.ErrorCodeInvalidRequest, "the envelope has the wrong shape")), nil
	}

	pairing, found, err := s.openPairing(ctx, body.UserCode)
	if err != nil {
		return nil, err
	}
	if !found {
		s.Limits.DeviceRelayIP.Fail(client)
		return api.RelayDeviceSettings404JSONResponse(coded(api.ErrorCodeInvalidUserCode, "no pairing is waiting for this code")), nil
	}
	if wait, stored := s.relays.put(pairing.ID, relayEnvelope{ct: body.Envelope.Ct, tag: body.Envelope.Tag}, pairing.ExpiresAt); !stored {
		return api.RelayDeviceSettings429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}
	return api.RelayDeviceSettings204Response{}, nil
}
