package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// Pairing timings. A pairing is open for ten minutes (RFC 8628 suggests a short life); the watch is told to poll
// every five seconds but a poll is only refused if it comes within three, so the jitter of a Bluetooth link does
// not cost it an answer. Approving gives the watch two more minutes to collect its token.
const (
	pairingLifetime = 10 * time.Minute
	pollInterval    = 5 * time.Second
	minPollGap      = 3 * time.Second
	collectGrace    = 2 * time.Minute

	maxDeviceNameRunes = 64
	defaultDeviceName  = "Watch"
)

// cleanName trims a name that comes from outside (a watch's idea of its own name, a person's label for a device)
// and says whether it can be stored: not too long, nothing that is not printable. Empty is allowed and means
// "not given".
func cleanName(name string) (cleaned string, ok bool) {
	cleaned = strings.TrimSpace(name)
	if utf8.RuneCountInString(cleaned) > maxDeviceNameRunes || !utf8.ValidString(cleaned) {
		return "", false
	}
	for _, r := range cleaned {
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	return cleaned, true
}

func seconds(d time.Duration) int { return int(d / time.Second) }

func (s *Server) newUserCode() (display, canonical string, err error) {
	if s.userCodes != nil {
		return s.userCodes()
	}
	return auth.NewUserCode()
}

// StartDevicePairing opens a pairing for a watch that has nothing yet: the codes it needs, and the page the
// person goes to. Every request is counted, since every one leaves a row.
func (s *Server) StartDevicePairing(ctx context.Context, request api.StartDevicePairingRequestObject) (api.StartDevicePairingResponseObject, error) {
	client := clientFrom(ctx)
	if wait, blocked := s.Limits.DeviceCodeIP.Blocked(client); blocked {
		return api.StartDevicePairing429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}
	s.Limits.DeviceCodeIP.Fail(client)

	var clientName *string
	if request.Body != nil && request.Body.ClientName != nil {
		name, ok := cleanName(*request.Body.ClientName)
		if !ok {
			return api.StartDevicePairing400JSONResponse(coded(api.ErrorCodeInvalidRequest, "clientName is too long or has unprintable characters")), nil
		}
		if name != "" {
			clientName = &name
		}
	}

	now := s.now()
	for range 5 {
		deviceCode, deviceHash, err := auth.NewDeviceCode()
		if err != nil {
			return nil, err
		}
		display, canonical, err := s.newUserCode()
		if err != nil {
			return nil, err
		}
		_, err = s.DB.CreatePairing(ctx, dbgen.CreatePairingParams{
			DeviceCodeHash: deviceHash, UserCode: canonical, ClientName: clientName,
			CreatedAt: now, ExpiresAt: now.Add(pairingLifetime),
		})
		if isUniqueViolation(err) {
			continue // the short code collided with a live one: draw another
		}
		if err != nil {
			return nil, fmt.Errorf("create pairing: %w", err)
		}
		page := s.DefaultServerURL + "/pair"
		return api.StartDevicePairing200JSONResponse{
			DeviceCode:              deviceCode,
			UserCode:                display,
			VerificationUri:         page,
			VerificationUriComplete: page + "?code=" + display,
			ExpiresIn:               seconds(pairingLifetime),
			Interval:                seconds(pollInterval),
		}, nil
	}
	return nil, errors.New("create pairing: no free user code")
}

// PollDevicePairing is the watch asking whether it was approved. Approval turns into a device and its token
// exactly once.
func (s *Server) PollDevicePairing(ctx context.Context, request api.PollDevicePairingRequestObject) (api.PollDevicePairingResponseObject, error) {
	client := clientFrom(ctx)
	if wait, blocked := s.Limits.DevicePollIP.Blocked(client); blocked {
		return api.PollDevicePairing429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}
	invalid := func() (api.PollDevicePairingResponseObject, error) {
		s.Limits.DevicePollIP.Fail(client)
		return api.PollDevicePairing400JSONResponse(coded(api.ErrorCodeInvalidDeviceCode, "unknown device code")), nil
	}
	if request.Body == nil || len(request.Body.DeviceCode) != auth.DeviceCodeLength {
		return invalid()
	}

	row, err := s.DB.GetPairingByDeviceCodeHash(ctx, auth.HashDeviceCode(request.Body.DeviceCode))
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return nil, fmt.Errorf("look up pairing: %w", err)
	}

	now := s.now()
	switch {
	case row.RedeemedAt != nil:
		// The token was handed out already. If that answer never reached the watch, it has to start over.
		return api.PollDevicePairing400JSONResponse(coded(api.ErrorCodeInvalidDeviceCode, "this pairing was already completed")), nil
	case !row.ExpiresAt.After(now):
		return api.PollDevicePairing400JSONResponse(coded(api.ErrorCodeExpiredToken, "the pairing ran out of time")), nil
	case row.LastPolledAt != nil && now.Sub(*row.LastPolledAt) < minPollGap:
		wait := minPollGap - now.Sub(*row.LastPolledAt)
		return api.PollDevicePairing429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}

	if row.ApprovedAt == nil {
		if err := s.DB.TouchPairingPoll(ctx, dbgen.TouchPairingPollParams{ID: row.ID, Now: now}); err != nil {
			return nil, fmt.Errorf("record poll: %w", err)
		}
		interval := seconds(pollInterval)
		return api.PollDevicePairing200JSONResponse{Status: api.PollPairingResultStatusPending, Interval: &interval}, nil
	}

	token, tokenHash, err := auth.NewDeviceToken()
	if err != nil {
		return nil, err
	}
	var created dbgen.CreateDeviceRow
	err = s.Tx(ctx, func(q dbgen.Querier) error {
		approval, err := q.RedeemPairing(ctx, dbgen.RedeemPairingParams{ID: row.ID, Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return errPairingCollected
		}
		if err != nil {
			return fmt.Errorf("redeem pairing: %w", err)
		}
		created, err = q.CreateDevice(ctx, dbgen.CreateDeviceParams{
			UserID: *approval.UserID, Name: *approval.DeviceName, TokenHash: tokenHash, CreatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("create device: %w", err)
		}
		return nil
	})
	if errors.Is(err, errPairingCollected) {
		// A second poll got there first; the token went to that answer.
		return api.PollDevicePairing400JSONResponse(coded(api.ErrorCodeInvalidDeviceCode, "this pairing was already completed")), nil
	}
	if err != nil {
		return nil, err
	}

	s.Logger.Info("device paired", "device_id", created.ID, "user_id", created.UserID)
	return api.PollDevicePairing200JSONResponse{Status: api.PollPairingResultStatusApproved, DeviceToken: &token, DeviceId: &created.ID}, nil
}

var errPairingCollected = errors.New("pairing already collected")

// GetDevicePairing tells the person which watch a code belongs to, before they approve it.
func (s *Server) GetDevicePairing(ctx context.Context, request api.GetDevicePairingRequestObject) (api.GetDevicePairingResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.GetDevicePairing401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	key := p.User.Id.String()
	if wait, blocked := s.Limits.PairingUser.Blocked(key); blocked {
		return api.GetDevicePairing429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}

	row, found, err := s.openPairing(ctx, request.UserCode)
	if err != nil {
		return nil, err
	}
	if !found {
		s.Limits.PairingUser.Fail(key)
		return api.GetDevicePairing404JSONResponse(coded(api.ErrorCodeInvalidUserCode, "no pairing is waiting for this code")), nil
	}
	return api.GetDevicePairing200JSONResponse{
		UserCode: auth.DisplayUserCode(row.UserCode), ClientName: row.ClientName, ExpiresAt: row.ExpiresAt,
	}, nil
}

// openPairing finds the pairing a typed user code belongs to, if one is still waiting for approval.
func (s *Server) openPairing(ctx context.Context, typed string) (dbgen.GetOpenPairingByUserCodeRow, bool, error) {
	canonical, ok := auth.ParseUserCode(typed)
	if !ok {
		return dbgen.GetOpenPairingByUserCodeRow{}, false, nil
	}
	row, err := s.DB.GetOpenPairingByUserCode(ctx, dbgen.GetOpenPairingByUserCodeParams{UserCode: canonical, Now: s.now()})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.GetOpenPairingByUserCodeRow{}, false, nil
	}
	if err != nil {
		return dbgen.GetOpenPairingByUserCodeRow{}, false, fmt.Errorf("look up pairing: %w", err)
	}
	return row, true, nil
}

// ApproveDevicePairing pairs the watch showing the code with the signed-in user.
func (s *Server) ApproveDevicePairing(ctx context.Context, request api.ApproveDevicePairingRequestObject) (api.ApproveDevicePairingResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.ApproveDevicePairing401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	body := request.Body
	if body == nil {
		return api.ApproveDevicePairing400JSONResponse(coded(api.ErrorCodeInvalidRequest, "expected a JSON body")), nil
	}
	key := p.User.Id.String()
	if wait, blocked := s.Limits.PairingUser.Blocked(key); blocked {
		return api.ApproveDevicePairing429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}

	name := ""
	if body.Name != nil {
		var ok bool
		if name, ok = cleanName(*body.Name); !ok {
			return api.ApproveDevicePairing400JSONResponse(coded(api.ErrorCodeInvalidRequest, "name is too long or has unprintable characters")), nil
		}
	}

	notFound := func() (api.ApproveDevicePairingResponseObject, error) {
		s.Limits.PairingUser.Fail(key)
		return api.ApproveDevicePairing404JSONResponse(coded(api.ErrorCodeInvalidUserCode, "no pairing is waiting for this code")), nil
	}
	pairing, found, err := s.openPairing(ctx, body.UserCode)
	if err != nil {
		return nil, err
	}
	if !found {
		return notFound()
	}
	if name == "" && pairing.ClientName != nil {
		name = *pairing.ClientName
	}
	if name == "" {
		name = defaultDeviceName
	}

	now := s.now()
	_, err = s.DB.ApprovePairing(ctx, dbgen.ApprovePairingParams{
		UserCode: pairing.UserCode, UserID: p.User.Id, DeviceName: name, Now: now, CollectUntil: now.Add(collectGrace),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound() // approved by someone else, or expired, since the look above
	}
	if err != nil {
		return nil, fmt.Errorf("approve pairing: %w", err)
	}
	s.Logger.Info("pairing approved", "user_id", p.User.Id)
	return api.ApproveDevicePairing204Response{}, nil
}

func (s *Server) ListDevices(ctx context.Context, _ api.ListDevicesRequestObject) (api.ListDevicesResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.ListDevices401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	rows, err := s.DB.ListDevices(ctx, p.User.Id)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	devices := make([]api.Device, 0, len(rows))
	for _, r := range rows {
		devices = append(devices, api.Device{Id: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt})
	}
	return api.ListDevices200JSONResponse{Devices: devices}, nil
}

func (s *Server) RevokeDevice(ctx context.Context, request api.RevokeDeviceRequestObject) (api.RevokeDeviceResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.RevokeDevice401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	_, err := s.DB.RevokeDevice(ctx, dbgen.RevokeDeviceParams{ID: request.Id, UserID: p.User.Id, Now: s.now()})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.RevokeDevice404JSONResponse(coded(api.ErrorCodeNotFound, "no such device")), nil
	}
	if err != nil {
		return nil, fmt.Errorf("revoke device: %w", err)
	}
	s.Logger.Info("device revoked", "device_id", request.Id, "user_id", p.User.Id)
	return api.RevokeDevice204Response{}, nil
}
