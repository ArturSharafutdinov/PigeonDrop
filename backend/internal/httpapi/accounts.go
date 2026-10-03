package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

const (
	maxCodeLength      = 128
	maxServerURLLength = 2048
)

const badServerURL = "customServerUrl must be null or an http(s) URL"

// PostAccount creates the account for a device code, or returns the existing one untouched.
func (s *Server) PostAccount(ctx context.Context, request api.PostAccountRequestObject) (api.PostAccountResponseObject, error) {
	if request.Body == nil || request.Body.Code == "" {
		return api.PostAccount400JSONResponse{Ok: false, Error: "expected { code }"}, nil
	}
	code := request.Body.Code
	if len(code) > maxCodeLength {
		return api.PostAccount400JSONResponse{Ok: false, Error: "code is too long"}, nil
	}

	row, err := s.DB.CreateAccount(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) { // it already existed: whoever created it first keeps it as it is
		row, err = s.DB.GetAccount(ctx, code)
	}
	if err != nil {
		return nil, fmt.Errorf("create account: %w", err)
	}
	return api.PostAccount200JSONResponse(s.account(row)), nil
}

func (s *Server) GetAccount(ctx context.Context, request api.GetAccountRequestObject) (api.GetAccountResponseObject, error) {
	row, err := s.DB.GetAccount(ctx, request.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.GetAccount404JSONResponse{Ok: false, Error: "account not found"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read account: %w", err)
	}
	return api.GetAccount200JSONResponse(s.account(row)), nil
}

// PutAccount sets the account's own server, or resets it with null. It never creates an account, and
// a body without the field is refused rather than treated as a reset.
func (s *Server) PutAccount(ctx context.Context, request api.PutAccountRequestObject) (api.PutAccountResponseObject, error) {
	var serverURL *string
	raw := bytes.TrimSpace(request.Body.CustomServerUrl)
	switch {
	case len(raw) == 0:
		return api.PutAccount400JSONResponse{Ok: false, Error: badServerURL}, nil
	case string(raw) == "null":
		// reset to this server
	default:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || !validServerURL(value) {
			return api.PutAccount400JSONResponse{Ok: false, Error: badServerURL}, nil
		}
		serverURL = &value
	}

	row, err := s.DB.SetAccountServerURL(ctx, dbgen.SetAccountServerURLParams{Code: request.Code, CustomServerUrl: serverURL})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.PutAccount404JSONResponse{Ok: false, Error: "account not found"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update account: %w", err)
	}
	return api.PutAccount200JSONResponse(s.account(row)), nil
}

// validServerURL is deliberately a light check, as the contract says: it must look like an http(s)
// URL, nothing more. (The server never fetches it - the watch's phone does.)
func validServerURL(value string) bool {
	return len(value) <= maxServerURLLength && (strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://"))
}

// account builds the API form of a stored account. serverUrl is computed, never stored: the account's
// own server when it has one, this server otherwise - so the watch never decides what "default" means.
func (s *Server) account(row dbgen.Account) api.Account {
	serverURL := s.DefaultServerURL
	if row.CustomServerUrl != nil {
		serverURL = *row.CustomServerUrl
	}
	return api.Account{
		Ok:              true,
		Code:            row.Code,
		CustomServerUrl: row.CustomServerUrl,
		ServerUrl:       serverURL,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}
