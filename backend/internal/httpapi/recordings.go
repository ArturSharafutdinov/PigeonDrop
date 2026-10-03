package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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

// The /v1 recordings. Unlike the prototype's /messages, each one belongs to a user and was sent by one of that
// user's devices. Files are named after the message id (<id>.opus as received, <id>.wav converted), so nothing
// a client chooses ever becomes a path.

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// plainText reports whether text from a client can be stored as it is: valid UTF-8 without control characters.
// (PostgreSQL cannot store a NUL in text at all, and a name is no place for a newline.)
func plainText(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsFunc(text, unicode.IsControl)
}

func opusFile(id uuid.UUID) string { return id.String() + ".opus" }
func wavFile(id uuid.UUID) string  { return id.String() + ".wav" }

// UploadMessage stores a recording for the device's owner, and remembers it under the device's own name for it so
// that a repeat of the same upload gets the same answer instead of a second copy.
func (s *Server) UploadMessage(ctx context.Context, request api.UploadMessageRequestObject) (api.UploadMessageResponseObject, error) {
	dev := deviceFrom(ctx)
	if dev == nil {
		return api.UploadMessage401JSONResponse(coded(api.ErrorCodeUnauthorized, "missing or invalid device token")), nil
	}
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
		return uploadMessageResult(stored.ID, stored.ClientMessageID, stored.BytesReceived, stored.ReceivedAt, true), nil
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

	id := uuid.New()
	if err := s.Messages.Put(opusFile(id), data); err != nil {
		return nil, fmt.Errorf("save recording: %w", err)
	}
	format := audio.Detect(data)
	hasWav, durationMs := s.convertMessage(id, data)

	row, err := s.DB.InsertMessage(ctx, dbgen.InsertMessageParams{
		ID: id, UserID: dev.UserID, DeviceID: dev.ID, ClientMessageID: body.MessageId, Format: string(format),
		BytesReceived: int64(len(data)), DurationMs: durationMs, HasWav: hasWav, ReceivedAt: s.now(),
	})
	switch {
	case err == nil:
		s.Logger.Info("saved recording", "message_id", id, "device_id", dev.ID, "bytes", len(data), "format", format, "wav", hasWav)
		return uploadMessageResult(row.ID, row.ClientMessageID, row.BytesReceived, row.ReceivedAt, false), nil
	case errors.Is(err, pgx.ErrNoRows):
		// An identical upload finished first. Keep its files, drop ours, and answer with its result.
		s.discardMessage(id, hasWav)
		winner, err := s.DB.GetMessageByClientID(ctx, dbgen.GetMessageByClientIDParams{DeviceID: dev.ID, ClientMessageID: body.MessageId})
		if err != nil {
			return nil, fmt.Errorf("read the winning upload: %w", err)
		}
		s.Logger.Info("concurrent duplicate upload, returning the first result", "device_id", dev.ID, "message_id", winner.ID)
		return uploadMessageResult(winner.ID, winner.ClientMessageID, winner.BytesReceived, winner.ReceivedAt, true), nil
	default:
		s.discardMessage(id, hasWav) // otherwise a retry would store the recording again and leave these behind
		return nil, fmt.Errorf("record message: %w", err)
	}
}

// convertMessage writes the WAV copy and reports whether there is one, and how long the audio is. The
// recording itself is already safe on disk; a conversion problem (a file that is not the expected format, a full
// disk) must not fail the whole upload, so it is logged and reported as "no WAV".
func (s *Server) convertMessage(id uuid.UUID, data []byte) (hasWav bool, durationMs *int32) {
	wav, err := audio.ToWAV(data)
	if err != nil {
		s.Logger.Warn("conversion failed", "message_id", id, "error", err)
		return false, nil
	}
	// 16 kHz, 16-bit, mono after a 44-byte header: 32 bytes per millisecond.
	ms := int32(max(len(wav)-44, 0) / 32)
	if err := s.Messages.Put(wavFile(id), wav); err != nil {
		s.Logger.Error("saving the converted file failed", "message_id", id, "error", err)
		return false, &ms
	}
	return true, &ms
}

func (s *Server) discardMessage(id uuid.UUID, hasWav bool) {
	names := []string{opusFile(id)}
	if hasWav {
		names = append(names, wavFile(id))
	}
	for _, name := range names {
		if err := s.Messages.Remove(name); err != nil && !errors.Is(err, storage.ErrNotFound) {
			s.Logger.Warn("could not remove a file", "file", name, "error", err)
		}
	}
}

func uploadMessageResult(id uuid.UUID, messageID string, bytes int64, receivedAt time.Time, duplicate bool) api.UploadMessageResponseObject {
	return api.UploadMessage200JSONResponse{
		Ok: true, Id: id, MessageId: messageID, BytesReceived: bytes, Duplicate: duplicate, ReceivedAt: receivedAt,
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
	s.discardMessage(row.ID, row.HasWav)
	s.Logger.Info("message deleted", "message_id", row.ID, "user_id", p.User.Id)
	return api.DeleteMessage204Response{}, nil
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
	// was received.
	var name, contentType, extension string
	switch {
	case format == "original" || (format == "" && !row.HasWav):
		name, extension = opusFile(id), "opus"
		contentType = "application/octet-stream"
		if row.Format == string(audio.FormatOggOpus) {
			contentType = "audio/ogg"
		}
	case row.HasWav:
		name, extension, contentType = wavFile(id), "wav", "audio/wav"
	default: // wav was asked for and does not exist
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

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Content-Disposition", fmt.Sprintf(`inline; filename="PigeonDrop-%s.%s"`, row.ReceivedAt.UTC().Format("2006-01-02_15-04-05"), extension))
	http.ServeContent(w, r, "", row.ReceivedAt, file)
}
