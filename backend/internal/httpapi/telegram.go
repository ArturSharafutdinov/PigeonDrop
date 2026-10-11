package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/telegram"
)

// TelegramBot is what the API needs of the bot (internal/telegram.Bot).
type TelegramBot interface {
	// Username is the bot's name without the @; empty until Telegram has said it, which means the bot cannot be
	// used yet.
	Username() string
}

// botUsername is the bot's name, or empty when there is no bot or it is not ready.
func (s *Server) botUsername() string {
	if s.Telegram == nil {
		return ""
	}
	return s.Telegram.Username()
}

// GetTelegram says whether recordings can go to Telegram and whether this account is linked to a chat.
func (s *Server) GetTelegram(ctx context.Context, _ api.GetTelegramRequestObject) (api.GetTelegramResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.GetTelegram401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	username := s.botUsername()
	if username == "" {
		return api.GetTelegram200JSONResponse{Available: false}, nil
	}

	status := api.TelegramStatus{Available: true, BotUsername: &username}
	row, err := s.DB.GetTelegramLink(ctx, p.User.Id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get the Telegram link: %w", err)
	}
	if err == nil {
		status.Link = &api.TelegramLink{Username: row.Username, FirstName: row.FirstName, LinkedAt: row.LinkedAt.UTC(), Blocked: row.BlockedAt != nil}
	}
	return api.GetTelegram200JSONResponse(status), nil
}

// CreateTelegramLink gives the person the link that links a Telegram chat to their account.
func (s *Server) CreateTelegramLink(ctx context.Context, _ api.CreateTelegramLinkRequestObject) (api.CreateTelegramLinkResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.CreateTelegramLink401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	username := s.botUsername()
	if username == "" {
		return api.CreateTelegramLink503JSONResponse(coded(api.ErrorCodeUnavailable, "the Telegram bot is off or not ready")), nil
	}
	key := p.User.Id.String()
	if wait, blocked := s.Limits.TelegramLinkUser.Blocked(key); blocked {
		return api.CreateTelegramLink429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}
	s.Limits.TelegramLinkUser.Fail(key)

	code, hash, err := telegram.NewLinkCode()
	if err != nil {
		return nil, err
	}
	now := s.now()
	expires := now.Add(telegram.LinkCodeLifetime)
	err = s.Tx(ctx, func(q dbgen.Querier) error {
		if err := q.DeleteUnusedTelegramLinkCodes(ctx, p.User.Id); err != nil {
			return fmt.Errorf("remove the earlier codes: %w", err)
		}
		if err := q.CreateTelegramLinkCode(ctx, dbgen.CreateTelegramLinkCodeParams{CodeHash: hash, UserID: p.User.Id, Now: now, ExpiresAt: expires}); err != nil {
			return fmt.Errorf("store the code: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create a Telegram link: %w", err)
	}
	return api.CreateTelegramLink200JSONResponse{Url: telegram.LinkURL(username, code), ExpiresAt: expires}, nil
}

// UnlinkTelegram removes the account's link to a chat. There being none is fine.
func (s *Server) UnlinkTelegram(ctx context.Context, _ api.UnlinkTelegramRequestObject) (api.UnlinkTelegramResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.UnlinkTelegram401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	removed, err := s.DB.DeleteTelegramLink(ctx, p.User.Id)
	if err != nil {
		return nil, fmt.Errorf("unlink Telegram: %w", err)
	}
	// A link that was asked for and not used yet must not link a chat after the person said stop.
	if err := s.DB.DeleteUnusedTelegramLinkCodes(ctx, p.User.Id); err != nil {
		return nil, fmt.Errorf("remove the unused Telegram codes: %w", err)
	}
	if removed > 0 {
		s.Logger.Info("telegram chat unlinked on the page", "user_id", p.User.Id)
	}
	return api.UnlinkTelegram204Response{}, nil
}
