package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/audio"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
)

// The recordings. Each one belongs to a user and was sent by one of that user's devices. The file is named
// after the message id (<id>.opus, as received), so nothing a client chooses ever becomes a path. The WAV a
// browser plays is made from it on each request and never stored: only the original takes room, and that is
// what an account's limit counts.

const (
	defaultPageSize = 50
	maxPageSize     = 100
	// maxFileNameLength is the longest messageId: the name the watch gave the recording.
	maxFileNameLength = 255
)

// plainText reports whether text from a client can be stored as it is: valid UTF-8 without control characters.
// (PostgreSQL cannot store a NUL in text at all, and a name is no place for a newline.)
func plainText(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsFunc(text, unicode.IsControl)
}

func opusFile(id uuid.UUID) string { return storage.RecordingName(id) }

// wavFile is the name an earlier version stored the converted copy under. Nothing writes it any more, but
// deleting a message still removes one if it is there.
func wavFile(id uuid.UUID) string { return id.String() + ".wav" }

// UploadMessage stores a recording for the device's owner, and remembers it under the device's own name for it so
// that a repeat of the same upload gets the same answer instead of a second copy.
//
// The recording is always accepted if it is within the size limit: when the account's storage is full, the
// account's oldest recordings are deleted to make room, and the answer tells the watch so (storage.state "full").
func (s *Server) UploadMessage(ctx context.Context, request api.UploadMessageRequestObject) (api.UploadMessageResponseObject, error) {
	dev := deviceFrom(ctx)
	if dev == nil {
		return api.UploadMessage401JSONResponse(coded(api.ErrorCodeUnauthorized, "missing or invalid device token")), nil
	}

	// Every request of a device counts, whatever becomes of it; a watch that is working properly is nowhere near.
	if wait, blocked := s.Limits.UploadDevice.Blocked(dev.ID.String()); blocked {
		s.Logger.Warn("device is uploading too often", "device_id", dev.ID, "user_id", dev.UserID, "retry_after", wait)
		return api.UploadMessage429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}
	s.Limits.UploadDevice.Fail(dev.ID.String())

	body := request.Body
	if body == nil || body.MessageId == "" || body.Audio == "" {
		return api.UploadMessage400JSONResponse(coded(api.ErrorCodeInvalidRequest, "expected { messageId, audio }")), nil
	}
	if len(body.MessageId) > maxFileNameLength {
		return api.UploadMessage400JSONResponse(coded(api.ErrorCodeInvalidRequest, "messageId is too long")), nil
	}
	if !plainText(body.MessageId) {
		return api.UploadMessage400JSONResponse(coded(api.ErrorCodeInvalidRequest, "messageId has invalid or control characters")), nil
	}

	// A repeat of an upload that already succeeded: the watch lost our answer, or a retry crossed it.
	stored, err := s.DB.GetMessageByClientID(ctx, dbgen.GetMessageByClientIDParams{DeviceID: dev.ID, ClientMessageID: body.MessageId})
	switch {
	case err == nil:
		s.Logger.Info("duplicate upload, returning the original result", "device_id", dev.ID, "message_id", stored.ID)
		return s.duplicateResult(ctx, dev.UserID, stored.ID, stored.ClientMessageID, stored.BytesReceived, stored.ReceivedAt)
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("look up message: %w", err)
	}

	data, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\r", "", "\n", "").Replace(body.Audio))
	if err != nil {
		return api.UploadMessage400JSONResponse(coded(api.ErrorCodeInvalidRequest, "audio is not valid base64")), nil
	}
	if len(data) == 0 {
		return api.UploadMessage400JSONResponse(coded(api.ErrorCodeInvalidRequest, "audio is empty")), nil
	}
	if len(data) > s.Storage.MaxAudioBytes {
		s.Logger.Warn("recording is over the size limit", "device_id", dev.ID, "bytes", len(data), "limit", s.Storage.MaxAudioBytes)
		return api.UploadMessage413JSONResponse{PayloadTooLargeJSONResponse: api.PayloadTooLargeJSONResponse(
			coded(api.ErrorCodePayloadTooLarge, fmt.Sprintf("a recording may be at most %d bytes", s.Storage.MaxAudioBytes)))}, nil
	}

	id := uuid.New()
	format := audio.Detect(data)
	decodable, durationMs, err := s.inspectRecording(ctx, id, data)
	if err != nil {
		return nil, err
	}
	if err := s.Messages.Put(opusFile(id), data); err != nil {
		return nil, fmt.Errorf("save recording: %w", err)
	}

	var (
		row    dbgen.InsertMessageRow
		pruned []uuid.UUID
		used   int64
		fresh  bool
	)
	err = s.Tx(ctx, func(q dbgen.Querier) error {
		// One upload of this account at a time decides whether there is room and what to delete for it.
		if err := q.LockUserStorage(ctx, dev.UserID); err != nil {
			return fmt.Errorf("lock storage: %w", err)
		}
		var err error
		row, err = q.InsertMessage(ctx, dbgen.InsertMessageParams{
			ID: id, UserID: dev.UserID, DeviceID: dev.ID, ClientMessageID: body.MessageId, Format: string(format),
			BytesReceived: int64(len(data)), DurationMs: durationMs, HasWav: decodable, ReceivedAt: s.now(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // an identical upload finished first (fresh stays false)
		}
		if err != nil {
			return fmt.Errorf("record message: %w", err)
		}
		fresh = true
		if s.Transcription != nil {
			// In this transaction, so that a recording is never accepted without its place in the queue
			// (and, if the upload fails after this point, never queued without being accepted).
			if err := s.Transcription.Enqueue(ctx, q, id, decodable, durationMs, s.now()); err != nil {
				return fmt.Errorf("queue the recording for recognition: %w", err)
			}
		}
		pruned, err = q.PruneMessages(ctx, dbgen.PruneMessagesParams{
			Owner: dev.UserID, KeepID: id, Room: s.Storage.AccountBytes - int64(len(data)),
		})
		if err != nil {
			return fmt.Errorf("make room: %w", err)
		}
		used, err = q.SumMessageBytes(ctx, dev.UserID)
		if err != nil {
			return fmt.Errorf("measure storage: %w", err)
		}
		return nil
	})
	if err != nil {
		s.discardMessage(id) // otherwise a retry would store the recording again and leave this behind
		return nil, err
	}

	if !fresh {
		// An identical upload finished first. Keep its file, drop ours, and answer with its result.
		s.discardMessage(id)
		winner, err := s.DB.GetMessageByClientID(ctx, dbgen.GetMessageByClientIDParams{DeviceID: dev.ID, ClientMessageID: body.MessageId})
		if err != nil {
			return nil, fmt.Errorf("read the winning upload: %w", err)
		}
		s.Logger.Info("concurrent duplicate upload, returning the first result", "device_id", dev.ID, "message_id", winner.ID)
		return s.duplicateResult(ctx, dev.UserID, winner.ID, winner.ClientMessageID, winner.BytesReceived, winner.ReceivedAt)
	}

	// The rows are gone for good; the files follow (a crash here leaves stray files, never a row pointing at nothing).
	for _, gone := range pruned {
		s.discardMessage(gone)
	}
	s.Logger.Info("saved recording", "message_id", id, "device_id", dev.ID, "bytes", len(data), "format", format, "decodable", decodable)
	if s.Transcription != nil && s.TranscriptionWake != nil {
		s.TranscriptionWake() // after the commit, so the worker finds the entry
	}
	if len(pruned) > 0 {
		s.Logger.Info("storage was full: the account's oldest recordings were deleted", "user_id", dev.UserID, "deleted", len(pruned), "used_bytes", used)
	}
	return api.UploadMessage200JSONResponse{
		Ok: true, Id: row.ID, MessageId: row.ClientMessageID, BytesReceived: row.BytesReceived, Duplicate: false,
		ReceivedAt: row.ReceivedAt, Storage: s.storageStatus(used, len(pruned)), Pruned: len(pruned),
	}, nil
}

// duplicateResult is the answer to an upload that was stored before. It reports the account's current storage too:
// the watch may not have seen the answer to the first upload.
func (s *Server) duplicateResult(ctx context.Context, userID, id uuid.UUID, messageID string, bytes int64, receivedAt time.Time) (api.UploadMessageResponseObject, error) {
	used, err := s.DB.SumMessageBytes(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("measure storage: %w", err)
	}
	return api.UploadMessage200JSONResponse{
		Ok: true, Id: id, MessageId: messageID, BytesReceived: bytes, Duplicate: true, ReceivedAt: receivedAt,
		Storage: s.storageStatus(used, 0), Pruned: 0,
	}, nil
}

// storageStatus says how full an account is. "full" is for the moment an upload had to delete older recordings;
// from WarnPercent of the limit the account is "nearly_full".
func (s *Server) storageStatus(used int64, pruned int) api.StorageStatus {
	limit := s.Storage.AccountBytes
	state := api.Ok
	switch {
	case pruned > 0:
		state = api.Full
	case used*100 >= limit*int64(s.Storage.WarnPercent):
		state = api.NearlyFull
	}
	return api.StorageStatus{UsedBytes: used, LimitBytes: limit, State: state}
}

// acquireDecodeSlot waits for one of the few places where a recording may be decoded; the returned function gives
// it back. It fails only if the request is cancelled while waiting.
func (s *Server) acquireDecodeSlot(ctx context.Context) (release func(), err error) {
	select {
	case s.decodeSlots <- struct{}{}:
		return func() { <-s.decodeSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// inspectRecording decodes a recording once, only to find out whether it can be decoded (so that it can be played
// later) and how long it is. Nothing is stored. A recording that cannot be decoded (a file that is not the expected
// format) is still kept, so a problem here is logged and reported as "not decodable", not as a failed upload.
func (s *Server) inspectRecording(ctx context.Context, id uuid.UUID, data []byte) (decodable bool, durationMs *int32, err error) {
	release, err := s.acquireDecodeSlot(ctx)
	if err != nil {
		return false, nil, err
	}
	defer release()

	wav, err := audio.ToWAV(data)
	if err != nil {
		s.Logger.Warn("recording cannot be decoded", "message_id", id, "error", err)
		return false, nil, nil
	}
	// 16 kHz, 16-bit, mono after a 44-byte header: 32 bytes per millisecond.
	ms := int32(max(len(wav)-44, 0) / 32)
	return true, &ms, nil
}

// discardMessage removes a message's files: the original and, if an earlier version left one, the converted copy.
func (s *Server) discardMessage(id uuid.UUID) {
	for _, name := range []string{opusFile(id), wavFile(id)} {
		if err := s.Messages.Remove(name); err != nil && !errors.Is(err, storage.ErrNotFound) {
			s.Logger.Warn("could not remove a file", "file", name, "error", err)
		}
	}
}

// The cursor of a page is the received_at and id of its last message, which is where the next page starts. It is
// opaque to clients: base64 of "<RFC 3339 time>|<id>".
func encodeCursor(receivedAt time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(receivedAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(cursor string) (time.Time, uuid.UUID, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	at, id, found := strings.Cut(string(raw), "|")
	if !found {
		return time.Time{}, uuid.Nil, false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	return t, u, true
}

func (s *Server) ListMessages(ctx context.Context, request api.ListMessagesRequestObject) (api.ListMessagesResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.ListMessages401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}

	limit := defaultPageSize
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	if limit < 1 || limit > maxPageSize {
		return api.ListMessages400JSONResponse(coded(api.ErrorCodeInvalidRequest, "limit must be from 1 to 100")), nil
	}
	query := dbgen.ListMessagesParams{UserID: p.User.Id, MaxRows: int32(limit + 1)} // one more than asked tells whether there is a next page
	if request.Params.Before != nil {
		at, id, ok := decodeCursor(*request.Params.Before)
		if !ok {
			return api.ListMessages400JSONResponse(coded(api.ErrorCodeInvalidRequest, "before is not a valid cursor")), nil
		}
		query.BeforeAt, query.BeforeID = &at, &id
	}

	rows, err := s.DB.ListMessages(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		cursor := encodeCursor(last.ReceivedAt, last.ID)
		next = &cursor
	}
	messages := make([]api.Message, 0, len(rows))
	for _, r := range rows {
		var duration *int
		if r.DurationMs != nil {
			d := int(*r.DurationMs)
			duration = &d
		}
		messages = append(messages, api.Message{
			Id: r.ID, MessageId: r.ClientMessageID, DeviceId: r.DeviceID, DeviceName: r.DeviceName,
			BytesReceived: r.BytesReceived, DurationMs: duration, HasWav: r.HasWav, ReceivedAt: r.ReceivedAt,
		})
	}
	return api.ListMessages200JSONResponse{Messages: messages, NextCursor: next}, nil
}

// DeleteMessage removes a recording the user owns, row first and then its files: a crash in between leaves
// stray files, never a row that points at nothing.
func (s *Server) DeleteMessage(ctx context.Context, request api.DeleteMessageRequestObject) (api.DeleteMessageResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.DeleteMessage401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	row, err := s.DB.DeleteMessage(ctx, dbgen.DeleteMessageParams{ID: request.Id, UserID: p.User.Id})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.DeleteMessage404JSONResponse(coded(api.ErrorCodeNotFound, "no such message")), nil
	}
	if err != nil {
		return nil, fmt.Errorf("delete message: %w", err)
	}
	s.discardMessage(row.ID)
	s.Logger.Info("message deleted", "message_id", row.ID, "user_id", p.User.Id)
	return api.DeleteMessage204Response{}, nil
}

// DeleteAllMessages empties the user's storage: every row first, then the files.
func (s *Server) DeleteAllMessages(ctx context.Context, _ api.DeleteAllMessagesRequestObject) (api.DeleteAllMessagesResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.DeleteAllMessages401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	ids, err := s.DB.DeleteAllMessages(ctx, p.User.Id)
	if err != nil {
		return nil, fmt.Errorf("delete all messages: %w", err)
	}
	for _, id := range ids {
		s.discardMessage(id)
	}
	s.Logger.Info("all messages deleted", "user_id", p.User.Id, "deleted", len(ids))
	return api.DeleteAllMessages200JSONResponse{Ok: true, Deleted: len(ids)}, nil
}

// GetStorage says how much of the limit the user's recordings take.
func (s *Server) GetStorage(ctx context.Context, _ api.GetStorageRequestObject) (api.GetStorageResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.GetStorage401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	used, err := s.DB.SumMessageBytes(ctx, p.User.Id)
	if err != nil {
		return nil, fmt.Errorf("measure storage: %w", err)
	}
	return api.GetStorage200JSONResponse(s.storageStatus(used, 0)), nil
}

// handleMessageAudio serves a recording to its owner, with range requests so that a browser's player can seek.
// It is written by hand (the "manual" tag in api/openapi.yaml) because the generated handlers speak JSON.
func (s *Server) handleMessageAudio(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		writeCodedError(w, http.StatusUnauthorized, api.ErrorCodeUnauthorized, "not signed in")
		return
	}
	notFound := func() { writeCodedError(w, http.StatusNotFound, api.ErrorCodeNotFound, "no such message") }

	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		notFound()
		return
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "wav" && format != "original" {
		writeCodedError(w, http.StatusBadRequest, api.ErrorCodeInvalidRequest, "format must be wav or original")
		return
	}
	row, err := s.DB.GetMessage(r.Context(), dbgen.GetMessageParams{ID: id, UserID: p.User.Id})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound()
		return
	}
	if err != nil {
		s.Logger.Error("look up message", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// The converted copy plays everywhere, so it is the default; the original is for those who want exactly what
	// was received. The converted copy is made from the original now, not read from disk: it is never stored.
	var name, contentType, extension string
	var asWAV bool
	switch {
	case format == "original" || (format == "" && !row.HasWav):
		name, extension = opusFile(id), "opus"
		contentType = "application/octet-stream"
		if row.Format == string(audio.FormatOggOpus) {
			contentType = "audio/ogg"
		}
	case row.HasWav:
		name, extension, contentType, asWAV = opusFile(id), "wav", "audio/wav", true
	default: // wav was asked for and the recording cannot be decoded
		notFound()
		return
	}

	file, err := s.Messages.Open(name)
	if errors.Is(err, storage.ErrNotFound) {
		s.Logger.Error("a message's file is missing", "message_id", id, "file", name)
		notFound()
		return
	}
	if err != nil {
		s.Logger.Error("open message file", "file", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer file.Close()

	var content io.ReadSeeker = file
	if asWAV {
		wav, err := s.decodeToWAV(r.Context(), file)
		if err != nil {
			if r.Context().Err() == nil {
				s.Logger.Error("convert message for playback", "message_id", id, "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
			return
		}
		content = bytes.NewReader(wav)
	}

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Content-Disposition", fmt.Sprintf(`inline; filename="PigeonDrop-%s.%s"`, row.ReceivedAt.UTC().Format("2006-01-02_15-04-05"), extension))
	http.ServeContent(w, r, "", row.ReceivedAt, content)
}

// handleMessagesArchive streams every recording of the user as one zip, each file as it was received and stored
// without compression (recordings are already compressed audio). Files are named after the time they were received,
// the zip after the time of the request, both in UTC like the single-file download. Written by hand because the
// generated handlers speak JSON.
func (s *Server) handleMessagesArchive(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		writeCodedError(w, http.StatusUnauthorized, api.ErrorCodeUnauthorized, "not signed in")
		return
	}
	rows, err := s.DB.ListMessageFiles(r.Context(), p.User.Id)
	if err != nil {
		s.Logger.Error("list message files", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(rows) == 0 {
		writeCodedError(w, http.StatusNotFound, api.ErrorCodeNotFound, "no recordings")
		return
	}

	const stamp = "2006-01-02_15-04-05"
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="PigeonDrop-%s.zip"`, s.now().UTC().Format(stamp)))

	// The archive can be the whole 100 MiB of an account, more than the server's write timeout allows on a slow link:
	// the deadline is moved forward for each file instead.
	rc := http.NewResponseController(w)
	zw := zip.NewWriter(w)
	taken := map[string]int{} // two recordings in one second would otherwise get one name
	for _, row := range rows {
		_ = rc.SetWriteDeadline(time.Now().Add(2 * time.Minute))
		if err := s.addToArchive(zw, row.ID, row.ReceivedAt, taken); err != nil {
			// The answer is already on its way, so all that is left is to cut the archive short: a zip without its
			// directory is rejected by unzip tools, which tells the person it is incomplete.
			s.Logger.Error("archive of recordings broke off", "user_id", p.User.Id, "message_id", row.ID, "error", err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.Logger.Warn("could not finish the archive", "user_id", p.User.Id, "error", err)
	}
}

// addToArchive copies one stored recording into the zip. A file that is gone (deleted since the list was read) is
// skipped; any other failure ends the archive.
func (s *Server) addToArchive(zw *zip.Writer, id uuid.UUID, receivedAt time.Time, taken map[string]int) error {
	file, err := s.Messages.Open(opusFile(id))
	if errors.Is(err, storage.ErrNotFound) {
		s.Logger.Warn("a message's file is missing, left out of the archive", "message_id", id)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer file.Close()

	base := "PigeonDrop-" + receivedAt.UTC().Format("2006-01-02_15-04-05")
	name := base + ".opus"
	if n := taken[base]; n > 0 {
		name = fmt.Sprintf("%s_%d.opus", base, n+1)
	}
	taken[base]++

	out, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: receivedAt})
	if err != nil {
		return fmt.Errorf("add %s: %w", name, err)
	}
	if _, err := io.Copy(out, file); err != nil {
		return fmt.Errorf("copy %s: %w", name, err)
	}
	return nil
}

// decodeToWAV reads a stored recording and makes the WAV that a browser plays. Few of these run at once.
func (s *Server) decodeToWAV(ctx context.Context, file io.Reader) ([]byte, error) {
	// The limit is far above what an upload may be; it only keeps a damaged or foreign file from filling memory.
	data, err := io.ReadAll(io.LimitReader(file, MaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read recording: %w", err)
	}
	release, err := s.acquireDecodeSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return audio.ToWAV(data)
}
