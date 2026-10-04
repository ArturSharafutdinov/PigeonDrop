package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/audio"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
)

const maxFileNameLength = 255

// PostMessage stores a recording, converts it to WAV, and remembers it under the watch's file
// name so that a repeat of the same upload gets the same answer instead of a second copy.
func (s *Server) PostMessage(ctx context.Context, request api.PostMessageRequestObject) (api.PostMessageResponseObject, error) {
	body := request.Body
	if body == nil || body.FileName == "" || body.Audio == "" {
		return badRequest("expected { fileName, audio }"), nil
	}
	if len(body.FileName) > maxFileNameLength {
		return badRequest("fileName is too long"), nil
	}
	if !plainText(body.FileName) {
		return badRequest("fileName has invalid or control characters"), nil
	}

	// A repeat of an upload that already succeeded: the watch lost our answer, or a retry crossed it.
	stored, err := s.DB.GetProcessedUpload(ctx, body.FileName)
	switch {
	case err == nil:
		s.Logger.Info("duplicate upload, returning the original result", "file", body.FileName, "saved_as", stored.SavedAs)
		return uploadResult(stored), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("look up upload: %w", err)
	}

	data, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\r", "", "\n", "").Replace(body.Audio))
	if err != nil {
		return badRequest("audio is not valid base64"), nil
	}
	if len(data) == 0 {
		return badRequest("audio is empty"), nil
	}

	opusName, err := s.saveRecording(data)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(opusName, ".opus")
	s.Logger.Info("saved recording", "file", body.FileName, "saved_as", opusName, "bytes", len(data), "format", audio.Detect(data))

	wavName := s.convertToWAV(base, data)

	row, err := s.DB.InsertProcessedUpload(ctx, dbgen.InsertProcessedUploadParams{
		FileName:      body.FileName,
		BytesReceived: int64(len(data)),
		SavedAs:       opusName,
		WavFile:       wavName,
	})
	switch {
	case err == nil:
		return uploadResult(row), nil
	case errors.Is(err, pgx.ErrNoRows):
		// An identical upload finished first. Keep its files, drop ours, and answer with its result.
		s.discard(opusName, wavName)
		winner, err := s.DB.GetProcessedUpload(ctx, body.FileName)
		if err != nil {
			return nil, fmt.Errorf("read the winning upload: %w", err)
		}
		s.Logger.Info("concurrent duplicate upload, returning the first result", "file", body.FileName, "saved_as", winner.SavedAs)
		return uploadResult(winner), nil
	default:
		s.discard(opusName, wavName) // otherwise a retry would store the recording again and leave these behind
		return nil, fmt.Errorf("record upload: %w", err)
	}
}

// saveRecording stores data as <time received>.opus. Two uploads in the same millisecond get a
// numeric suffix instead of overwriting each other.
func (s *Server) saveRecording(data []byte) (string, error) {
	stem := receivedAtName(s.now())
	for attempt := 0; attempt < 100; attempt++ {
		name := stem + ".opus"
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d.opus", stem, attempt)
		}
		switch err := s.Files.Put(name, data); {
		case err == nil:
			return name, nil
		case errors.Is(err, storage.ErrExists):
			continue
		default:
			return "", fmt.Errorf("save recording: %w", err)
		}
	}
	return "", errors.New("save recording: no free file name")
}

// convertToWAV returns the WAV file's name, or nil when the recording could not be converted.
// The recording itself is already safe on disk; a conversion problem (a file that is not the
// expected format, a full disk) must not fail the whole upload, so it is logged and reported as
// a null wavFile.
func (s *Server) convertToWAV(base string, data []byte) *string {
	wav, err := audio.ToWAV(data)
	if err != nil {
		s.Logger.Warn("conversion failed", "file", base+".opus", "error", err)
		return nil
	}
	name := base + ".wav"
	if err := s.Files.Put(name, wav); err != nil {
		s.Logger.Error("saving the converted file failed", "file", name, "error", err)
		return nil
	}
	return &name
}

func (s *Server) discard(opusName string, wavName *string) {
	if err := s.Files.Remove(opusName); err != nil {
		s.Logger.Warn("could not remove a duplicate file", "file", opusName, "error", err)
	}
	if wavName != nil {
		if err := s.Files.Remove(*wavName); err != nil {
			s.Logger.Warn("could not remove a duplicate file", "file", *wavName, "error", err)
		}
	}
}

// receivedAtName is "2026-10-03_07-23-36-048": sortable, free of characters filesystems dislike,
// and precise to the millisecond.
func receivedAtName(t time.Time) string {
	return fmt.Sprintf("%s-%03d", t.Format("2006-01-02_15-04-05"), t.Nanosecond()/int(time.Millisecond))
}

func badRequest(message string) api.PostMessageResponseObject {
	return api.PostMessage400JSONResponse{Ok: false, Error: message}
}

func uploadResult(row dbgen.ProcessedUpload) api.PostMessageResponseObject {
	return api.PostMessage200JSONResponse{
		Ok:            true,
		BytesReceived: row.BytesReceived,
		SavedAs:       row.SavedAs,
		WavFile:       row.WavFile,
		ReceivedAt:    row.ReceivedAt,
	}
}
