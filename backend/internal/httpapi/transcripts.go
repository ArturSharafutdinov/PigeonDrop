package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// The text of a recording as people see it: in the list of messages (the field transcript), in the archive of all
// recordings (a .txt next to the audio), and a way to ask for another try when the text could not be made. The
// queue and the model are in internal/transcribe; this is only what the API shows of them.

// transcriptOf turns the columns of a queue row (all null for a recording that was never queued) into the API's
// transcript, or nil for such a recording.
func transcriptOf(state, text, language, engine *string, finishedAt *time.Time, errorCode *string) *api.Transcript {
	if state == nil {
		return nil
	}
	t := &api.Transcript{State: api.TranscriptState(*state), Text: text, Language: language, Engine: engine}
	if finishedAt != nil {
		utc := finishedAt.UTC() // the driver gives the server's local time; the API speaks UTC
		t.FinishedAt = &utc
	}
	if errorCode != nil {
		code := api.TranscriptErrorCode(*errorCode)
		t.ErrorCode = &code
	}
	return t
}

// RetryTranscription puts a recording whose text could not be made back in the queue. Only the owner's own
// recording, and only one that was given up on: whatever else it is (waiting, being recognised, done, not going to
// the model) a second go would change nothing.
func (s *Server) RetryTranscription(ctx context.Context, request api.RetryTranscriptionRequestObject) (api.RetryTranscriptionResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.RetryTranscription401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	notFound := api.RetryTranscription404JSONResponse(coded(api.ErrorCodeNotFound, "no such message"))

	if _, err := s.DB.GetMessage(ctx, dbgen.GetMessageParams{ID: request.Id, UserID: p.User.Id}); errors.Is(err, pgx.ErrNoRows) {
		return notFound, nil
	} else if err != nil {
		return nil, fmt.Errorf("look up message: %w", err)
	}
	if s.Transcription == nil {
		// With nothing to work the queue, a recording put back in it would wait for ever.
		return api.RetryTranscription409JSONResponse(coded(api.ErrorCodeConflict, "speech recognition is switched off on this server")), nil
	}

	queued, err := s.DB.RetryOwnTranscription(ctx, dbgen.RetryOwnTranscriptionParams{MessageID: request.Id, UserID: p.User.Id, Now: s.now()})
	if err != nil {
		return nil, fmt.Errorf("retry transcription: %w", err)
	}
	if queued == 0 {
		return api.RetryTranscription409JSONResponse(coded(api.ErrorCodeConflict, "this recording has no failed transcription to retry")), nil
	}

	// Read back rather than assume: the worker may have taken it already.
	row, err := s.DB.GetOwnTranscription(ctx, dbgen.GetOwnTranscriptionParams{MessageID: request.Id, UserID: p.User.Id})
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound, nil // deleted this very moment
	}
	if err != nil {
		return nil, fmt.Errorf("read transcription: %w", err)
	}
	s.Logger.Info("transcription retried", "message_id", request.Id, "user_id", p.User.Id)
	if s.TranscriptionWake != nil {
		s.TranscriptionWake()
	}
	return api.RetryTranscription202JSONResponse(*transcriptOf(&row.State, row.Text, row.Language, row.Engine, row.FinishedAt, row.ErrorCode)), nil
}
