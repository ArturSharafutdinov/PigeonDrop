package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// Session lifetimes. A session lives while it is used (idle limit, renewed at most once per
// sessionTouchEvery) but never past the absolute limit, so a stolen cookie cannot be kept alive forever.
const (
	sessionIdleTTL     = 30 * 24 * time.Hour
	sessionAbsoluteTTL = 90 * 24 * time.Hour
	sessionTouchEvery  = time.Hour
	// A device's last_seen_at is refreshed at most this often, so a busy watch does not write on every request.
	deviceSeenEvery = time.Hour
)

// The cookie's name. Over HTTPS it carries the __Host- prefix, which makes browsers refuse to accept it from
// any other host or path; the prefix is not allowed without Secure, so plain-http development uses the bare name.
const (
	cookieName       = "pd_session"
	secureCookieName = "__Host-" + cookieName
)

// TxFunc runs fn in one database transaction (see db.InTx).
type TxFunc func(ctx context.Context, fn func(q dbgen.Querier) error) error

// AuthLimits are the brute-force limiters. NewHandler fills in the defaults.
type AuthLimits struct {
	// LoginEmail counts failed sign-ins per email address, whether or not it is registered, so the count
	// does not show which addresses exist.
	LoginEmail *auth.Limiter
	// LoginIP counts failed sign-ins per client address.
	LoginIP *auth.Limiter
	// RegisterIP counts rejected invitation codes per client address.
	RegisterIP *auth.Limiter
	// DeviceCodeIP counts every request for a pairing per client address: unlike the others, a success is
	// what is being limited, since each one leaves a row behind.
	DeviceCodeIP *auth.Limiter
	// DevicePollIP counts polls with a device code that does not exist, per client address.
	DevicePollIP *auth.Limiter
	// PairingUser counts a signed-in user's wrong user codes (looking up or approving a pairing).
	PairingUser *auth.Limiter
	// UploadDevice counts every upload a device makes (counted whatever the answer was, repeats included), so
	// that a watch stuck in a loop, or a stolen token, cannot push a flood of files and, through the account's
	// automatic cleanup, push the person's real recordings out.
	UploadDevice *auth.Limiter
	// TelegramLinkUser counts every link a signed-in user asks for to link a Telegram chat (a success leaves a row,
	// like a pairing does).
	TelegramLinkUser *auth.Limiter
}

func defaultLimits(now func() time.Time) AuthLimits {
	return AuthLimits{
		LoginEmail: auth.NewLimiter(5, 15*time.Minute, now),
		LoginIP:    auth.NewLimiter(20, 15*time.Minute, now),
		RegisterIP: auth.NewLimiter(10, time.Hour, now),

		DeviceCodeIP: auth.NewLimiter(10, 10*time.Minute, now),
		DevicePollIP: auth.NewLimiter(30, 15*time.Minute, now),
		PairingUser:  auth.NewLimiter(10, 15*time.Minute, now),

		UploadDevice: auth.NewLimiter(60, time.Hour, now),

		TelegramLinkUser: auth.NewLimiter(10, time.Hour, now),
	}
}

type ctxKey int

const (
	ctxPrincipal ctxKey = iota
	ctxDevice
	ctxClient
)

// principal is the signed-in user behind a request's session cookie.
type principal struct {
	SessionID uuid.UUID
	TokenHash []byte
	User      api.User
}

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxPrincipal).(*principal)
	return p
}

// device is the paired watch behind a request's device token.
type device struct {
	ID     uuid.UUID
	UserID uuid.UUID
	Name   string
}

func deviceFrom(ctx context.Context) *device {
	d, _ := ctx.Value(ctxDevice).(*device)
	return d
}

// clientFrom returns the key the request's sender is counted under (see clientKey).
func clientFrom(ctx context.Context) string {
	k, _ := ctx.Value(ctxClient).(string)
	return k
}

func coded(code api.ErrorCode, message string) api.Error {
	return api.Error{Ok: false, Error: message, Code: &code}
}

func writeCodedError(w http.ResponseWriter, status int, code api.ErrorCode, message string) {
	writeErrorBody(w, status, coded(code, message))
}

func tooManyAttempts(wait time.Duration) api.TooManyAttemptsJSONResponse {
	seconds := max(int(math.Ceil(wait.Seconds())), 1)
	return api.TooManyAttemptsJSONResponse{
		Body:    coded(api.ErrorCodeTooManyAttempts, "too many failed attempts, try again later"),
		Headers: api.TooManyAttemptsResponseHeaders{RetryAfter: &seconds},
	}
}

func unauthorized() api.UnauthorizedJSONResponse {
	return api.UnauthorizedJSONResponse(coded(api.ErrorCodeUnauthorized, "not signed in"))
}

// clientKey identifies the sender for the limits: its IP address, or for IPv6 its /64 network, since one
// subscriber usually holds a whole /64 and could otherwise change address with every attempt. Behind a
// reverse proxy (TrustProxyHeaders) it is the last address in X-Forwarded-For, the one the proxy itself added.
func (s *Server) clientKey(r *http.Request) string {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remote = r.RemoteAddr
	}
	candidates := []string{remote}
	if s.TrustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			candidates = []string{strings.TrimSpace(xff[strings.LastIndexByte(xff, ',')+1:]), remote}
		}
	}
	for _, c := range candidates {
		addr, err := netip.ParseAddr(c)
		if err != nil {
			continue
		}
		addr = addr.Unmap().WithZone("")
		if addr.Is6() {
			prefix, _ := addr.Prefix(64)
			return prefix.String()
		}
		return addr.String()
	}
	return remote
}

func (s *Server) cookieName() string {
	if s.CookieSecure {
		return secureCookieName
	}
	return cookieName
}

// sessionCookie is the Set-Cookie value that gives the browser its session token for ttl.
func (s *Server) sessionCookie(token string, ttl time.Duration) string {
	return (&http.Cookie{
		Name: s.cookieName(), Value: token, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode,
	}).String()
}

func (s *Server) clearedCookie() string {
	return (&http.Cookie{
		Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode,
	}).String()
}

// crossOriginGuard refuses requests that a web page on another site sends to the /v1 API on a visitor's
// behalf (CSRF), using the standard library's check of the Sec-Fetch-Site and Origin headers. Requests
// that carry neither - curl, the apps - pass, and the session cookie is SameSite=Lax besides.
func (s *Server) crossOriginGuard(next http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	for _, origin := range s.AllowedOrigins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			s.Logger.Warn("ignoring a trusted origin", "origin", origin, "error", err)
		}
	}
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeCodedError(w, http.StatusForbidden, api.ErrorCodeForbiddenOrigin, "cross-origin request refused")
	}))
	guarded := protection.Handler(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			guarded.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authContext prepares every /v1 request: it notes who is sending it (for the limits), who is signed in if
// the request carries a valid session cookie, and which watch it is if it carries a valid device token.
// Handlers that need a user or a device check for one themselves.
func (s *Server) authContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), ctxClient, s.clientKey(r))

		ctx, err := s.withSession(ctx, w, r)
		if err == nil {
			ctx, err = s.withDevice(ctx, r)
		}
		if err != nil {
			s.Logger.Error("look up credentials", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withSession adds the signed-in user to ctx if the request has a valid session cookie. An unknown or expired
// session is the same as none: the handler answers 401 where it matters.
func (s *Server) withSession(ctx context.Context, w http.ResponseWriter, r *http.Request) (context.Context, error) {
	c, err := r.Cookie(s.cookieName())
	if err != nil || len(c.Value) != auth.SessionTokenLength {
		return ctx, nil
	}
	now := s.now()
	hash := auth.HashSessionToken(c.Value)
	row, err := s.DB.GetSessionByTokenHash(ctx, dbgen.GetSessionByTokenHashParams{TokenHash: hash, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return ctx, nil
	}
	if err != nil {
		return ctx, fmt.Errorf("session: %w", err)
	}

	// A session in use keeps sliding forward, and the browser's copy of the cookie with it.
	if now.Sub(row.LastSeenAt) >= sessionTouchEvery {
		expires := now.Add(sessionIdleTTL)
		if err := s.DB.TouchSession(ctx, dbgen.TouchSessionParams{ID: row.SessionID, Now: now, NewExpiresAt: expires}); err != nil {
			s.Logger.Warn("renew session", "error", err) // not worth failing the request over
		} else {
			w.Header().Add("Set-Cookie", s.sessionCookie(c.Value, min(sessionIdleTTL, row.AbsoluteExpiresAt.Sub(now))))
		}
	}
	return context.WithValue(ctx, ctxPrincipal, &principal{
		SessionID: row.SessionID,
		TokenHash: hash,
		User: api.User{
			Id: row.UserID, Email: row.Email, Plan: api.UserPlan(row.Plan),
			Language: api.Language(row.Language), CreatedAt: row.UserCreatedAt,
		},
	}), nil
}

// withDevice adds the paired watch to ctx if the request has a valid, unrevoked device token.
func (s *Server) withDevice(ctx context.Context, r *http.Request) (context.Context, error) {
	token, ok := auth.ParseBearer(r.Header.Get("Authorization"))
	if !ok {
		return ctx, nil
	}
	row, err := s.DB.GetDeviceByTokenHash(ctx, auth.HashDeviceToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return ctx, nil
	}
	if err != nil {
		return ctx, fmt.Errorf("device: %w", err)
	}

	if now := s.now(); row.LastSeenAt == nil || now.Sub(*row.LastSeenAt) >= deviceSeenEvery {
		if err := s.DB.TouchDevice(ctx, dbgen.TouchDeviceParams{ID: row.ID, Now: now, RecentAfter: now.Add(-deviceSeenEvery)}); err != nil {
			s.Logger.Warn("record device activity", "device_id", row.ID, "error", err)
		}
	}
	return context.WithValue(ctx, ctxDevice, &device{ID: row.ID, UserID: row.UserID, Name: row.Name}), nil
}

// startSession stores a new session for the user and returns the cookie that carries it.
func (s *Server) startSession(ctx context.Context, q dbgen.Querier, userID uuid.UUID) (cookie string, err error) {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	if _, err := q.CreateSession(ctx, dbgen.CreateSessionParams{
		UserID: userID, TokenHash: hash, CreatedAt: now,
		ExpiresAt: now.Add(sessionIdleTTL), AbsoluteExpiresAt: now.Add(sessionAbsoluteTTL),
	}); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return s.sessionCookie(token, sessionIdleTTL), nil
}

func userOf(u dbgen.User) api.User {
	return api.User{Id: u.ID, Email: u.Email, Plan: api.UserPlan(u.Plan), Language: api.Language(u.Language), CreatedAt: u.CreatedAt}
}

var (
	errEmailTaken = errors.New("email already registered")
	errInviteGone = errors.New("invitation is no longer usable")
)

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Register creates the account and starts its session, spending the invitation in the same transaction:
// either both happen or neither does, and two people racing for one code cannot both get an account.
func (s *Server) Register(ctx context.Context, request api.RegisterRequestObject) (api.RegisterResponseObject, error) {
	client := clientFrom(ctx)
	if wait, blocked := s.Limits.RegisterIP.Blocked(client); blocked {
		return api.Register429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}
	body := request.Body
	if body == nil {
		return api.Register400JSONResponse(coded(api.ErrorCodeInvalidRequest, "expected a JSON body")), nil
	}

	email, err := auth.NormalizeEmail(body.Email)
	if err != nil {
		return api.Register400JSONResponse(coded(api.ErrorCodeInvalidEmail, "not a valid email address")), nil
	}
	if err := auth.ValidatePassword(body.Password); err != nil {
		return api.Register400JSONResponse(coded(api.ErrorCodeWeakPassword, err.Error())), nil
	}
	language := api.Language("en")
	if body.Language != nil {
		language = *body.Language
	}
	if language != "en" && language != "ru" {
		return api.Register400JSONResponse(coded(api.ErrorCodeInvalidRequest, "language must be en or ru")), nil
	}

	invalidInvite := func() (api.RegisterResponseObject, error) {
		s.Limits.RegisterIP.Fail(client)
		return api.Register403JSONResponse(coded(api.ErrorCodeInvalidInvite, "invitation code is not valid")), nil
	}
	codeHash, ok := auth.ParseInviteCode(body.InviteCode)
	if !ok {
		return invalidInvite()
	}
	// Look before hashing: a made-up code should not cost the server a password hash.
	if _, err := s.DB.FindUsableInvitation(ctx, dbgen.FindUsableInvitationParams{CodeHash: codeHash, Now: s.now()}); errors.Is(err, pgx.ErrNoRows) {
		return invalidInvite()
	} else if err != nil {
		return nil, fmt.Errorf("look up invitation: %w", err)
	}

	passwordHash, err := s.Hasher.Hash(ctx, body.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	var (
		user   dbgen.User
		cookie string
	)
	err = s.Tx(ctx, func(q dbgen.Querier) error {
		var err error
		user, err = q.CreateUser(ctx, dbgen.CreateUserParams{Email: email, PasswordHash: passwordHash, Language: string(language), CreatedAt: s.now()})
		if isUniqueViolation(err) {
			return errEmailTaken
		}
		if err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		if _, err := q.ConsumeInvitation(ctx, dbgen.ConsumeInvitationParams{CodeHash: codeHash, Now: s.now(), UsedBy: &user.ID}); errors.Is(err, pgx.ErrNoRows) {
			return errInviteGone // somebody else used it between the look above and now: the user row rolls back
		} else if err != nil {
			return fmt.Errorf("spend invitation: %w", err)
		}
		cookie, err = s.startSession(ctx, q, user.ID)
		return err
	})
	switch {
	case errors.Is(err, errEmailTaken):
		return api.Register409JSONResponse(coded(api.ErrorCodeEmailTaken, "this email is already registered")), nil
	case errors.Is(err, errInviteGone):
		return invalidInvite()
	case err != nil:
		return nil, err
	}

	s.Logger.Info("user registered", "user_id", user.ID)
	return api.Register201JSONResponse{Body: userOf(user), Headers: api.Register201ResponseHeaders{SetCookie: &cookie}}, nil
}

// Login checks the password and starts a session. Every failure looks the same from outside: the same
// answer, and (for an unregistered email) the same time, since a password hash is spent either way.
func (s *Server) Login(ctx context.Context, request api.LoginRequestObject) (api.LoginResponseObject, error) {
	client := clientFrom(ctx)
	body := request.Body
	if body == nil {
		return api.Login400JSONResponse(coded(api.ErrorCodeInvalidRequest, "expected a JSON body")), nil
	}
	badCredentials := api.Login401JSONResponse(coded(api.ErrorCodeInvalidCredentials, "wrong email or password"))

	if wait, blocked := s.Limits.LoginIP.Blocked(client); blocked {
		return api.Login429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}
	email, err := auth.NormalizeEmail(body.Email)
	if err != nil {
		s.Limits.LoginIP.Fail(client)
		return badCredentials, nil
	}
	if wait, blocked := s.Limits.LoginEmail.Blocked(email); blocked {
		return api.Login429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}
	fail := func() (api.LoginResponseObject, error) {
		s.Limits.LoginIP.Fail(client)
		s.Limits.LoginEmail.Fail(email)
		return badCredentials, nil
	}

	user, err := s.DB.GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := s.Hasher.VerifyUnknown(ctx, body.Password); err != nil {
			return nil, fmt.Errorf("verify password: %w", err)
		}
		return fail()
	}
	if err != nil {
		return nil, fmt.Errorf("look up user: %w", err)
	}

	matches, needsRehash, err := s.Hasher.Verify(ctx, user.PasswordHash, body.Password)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if !matches {
		return fail()
	}
	// The address starts afresh. The sender's own count (by IP) stays, so someone guessing at other accounts
	// cannot clear it by signing in to one they own.
	s.Limits.LoginEmail.Reset(email)
	if needsRehash {
		s.upgradeHash(ctx, user.ID, body.Password)
	}

	cookie, err := s.startSession(ctx, s.DB, user.ID)
	if err != nil {
		return nil, err
	}
	return api.Login200JSONResponse{Body: userOf(user), Headers: api.Login200ResponseHeaders{SetCookie: &cookie}}, nil
}

// upgradeHash stores the password again under the current hash settings, once the right password is in hand.
// It is an improvement and not part of signing in, so a failure is logged and nothing more.
func (s *Server) upgradeHash(ctx context.Context, userID uuid.UUID, password string) {
	hash, err := s.Hasher.Hash(ctx, password)
	if err == nil {
		err = s.DB.SetUserPassword(ctx, dbgen.SetUserPasswordParams{ID: userID, PasswordHash: hash})
	}
	if err != nil {
		s.Logger.Warn("upgrade password hash", "user_id", userID, "error", err)
	}
}

// Logout ends the session of the cookie it came with, if any. It never fails for want of one.
func (s *Server) Logout(ctx context.Context, _ api.LogoutRequestObject) (api.LogoutResponseObject, error) {
	if p := principalFrom(ctx); p != nil {
		if err := s.DB.DeleteSessionByTokenHash(ctx, p.TokenHash); err != nil {
			return nil, fmt.Errorf("end session: %w", err)
		}
	}
	cookie := s.clearedCookie()
	return api.Logout204Response{Headers: api.Logout204ResponseHeaders{SetCookie: &cookie}}, nil
}

func (s *Server) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.GetMe401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	return api.GetMe200JSONResponse(p.User), nil
}

// ChangePassword replaces the password and ends the user's other sessions, so whoever else was signed in
// (a forgotten browser, a thief with an old cookie) is out; the session making the request stays.
func (s *Server) ChangePassword(ctx context.Context, request api.ChangePasswordRequestObject) (api.ChangePasswordResponseObject, error) {
	p := principalFrom(ctx)
	if p == nil {
		return api.ChangePassword401JSONResponse{UnauthorizedJSONResponse: unauthorized()}, nil
	}
	body := request.Body
	if body == nil {
		return api.ChangePassword400JSONResponse(coded(api.ErrorCodeInvalidRequest, "expected a JSON body")), nil
	}
	if err := auth.ValidatePassword(body.NewPassword); err != nil {
		return api.ChangePassword400JSONResponse(coded(api.ErrorCodeWeakPassword, err.Error())), nil
	}
	// A stolen session must not be a way around the sign-in limits: wrong attempts here count against the
	// same address.
	if wait, blocked := s.Limits.LoginEmail.Blocked(p.User.Email); blocked {
		return api.ChangePassword429JSONResponse{TooManyAttemptsJSONResponse: tooManyAttempts(wait)}, nil
	}

	user, err := s.DB.GetUserByID(ctx, p.User.Id)
	if err != nil {
		return nil, fmt.Errorf("look up user: %w", err)
	}
	matches, _, err := s.Hasher.Verify(ctx, user.PasswordHash, body.CurrentPassword)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if !matches {
		s.Limits.LoginEmail.Fail(p.User.Email)
		return api.ChangePassword403JSONResponse(coded(api.ErrorCodeWrongPassword, "the current password is wrong")), nil
	}
	s.Limits.LoginEmail.Reset(p.User.Email)

	newHash, err := s.Hasher.Hash(ctx, body.NewPassword)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	err = s.Tx(ctx, func(q dbgen.Querier) error {
		if err := q.SetUserPassword(ctx, dbgen.SetUserPasswordParams{ID: user.ID, PasswordHash: newHash}); err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if err := q.DeleteOtherSessions(ctx, dbgen.DeleteOtherSessionsParams{UserID: user.ID, ID: p.SessionID}); err != nil {
			return fmt.Errorf("end other sessions: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.Logger.Info("password changed", "user_id", user.ID)
	return api.ChangePassword204Response{}, nil
}
