package auth

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// User is the public Console user view. UID is serialized as id, while the
// internal auto-increment database key is never exposed.
type User struct {
	UID         string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// Service coordinates Console authentication across PostgreSQL and Redis.
type Service struct {
	queries         *sqlc.Queries
	verification    *VerificationStore
	sessions        *SessionManager
	logger          *zap.Logger
	emailCheckDelay func() time.Duration
}

// NewService creates the Console authentication service.
func NewService(
	db consoleservice.DB,
	verification *VerificationStore,
	sessions *SessionManager,
	logger *zap.Logger,
) (*Service, error) {
	if db == nil || verification == nil || sessions == nil {
		return nil, errors.New("console authentication dependencies are incomplete")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{
		queries:         sqlc.New(db),
		verification:    verification,
		sessions:        sessions,
		logger:          logger,
		emailCheckDelay: randomEmailCheckDelay,
	}, nil
}

// SendChallenge issues a purpose-bound email verification challenge.
func (s *Service) SendChallenge(
	ctx context.Context,
	rawEmail string,
	rawPurpose string,
	ip string,
) (Challenge, *consoleservice.Error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return Challenge{}, err
	}
	purpose, err := ParsePurpose(rawPurpose)
	if err != nil {
		return Challenge{}, err
	}
	return s.verification.Issue(ctx, email, purpose, ip)
}

// Register verifies an email challenge, creates the user, and starts a session.
func (s *Service) Register(
	ctx context.Context,
	rawEmail string,
	password string,
	challengeID string,
	code string,
	ip string,
) (User, TokenPair, *consoleservice.Error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return User{}, TokenPair{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, TokenPair{}, err
	}

	reservation, reserveErr := s.verification.Reserve(ctx, email, PurposeRegister, ip, challengeID, code)
	if reserveErr != nil {
		return User{}, TokenPair{}, reserveErr
	}
	release := true
	defer func() {
		if release {
			_ = s.verification.Release(context.Background(), email, PurposeRegister, reservation)
		}
	}()

	hash, hashErr := HashPassword(password)
	if hashErr != nil {
		return User{}, TokenPair{}, requestUnavailable("hash registration password", hashErr)
	}
	uid, uidErr := uuid.NewV7()
	if uidErr != nil {
		return User{}, TokenPair{}, requestUnavailable("generate public user id", uidErr)
	}
	row, createErr := s.queries.CreateConsoleUser(ctx, sqlc.CreateConsoleUserParams{
		Uid:          pgUUID(uid),
		Email:        email,
		PasswordHash: hash,
		DisplayName:  defaultDisplayName(email),
	})
	if createErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(createErr, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_users_email_lower" {
			return User{}, TokenPair{}, registrationUnavailable()
		}
		return User{}, TokenPair{}, requestUnavailable("create console user", createErr)
	}
	user := userFromCreateRow(row)
	release = false
	if commitChallengeErr := s.verification.Commit(ctx, email, PurposeRegister, reservation); commitChallengeErr != nil {
		s.logger.Warn("registration committed but challenge finalization failed", zap.Error(commitChallengeErr), zap.String("user_uid", user.UID))
	}
	pair, sessionErr := s.sessions.Create(ctx, user.UID)
	return user, pair, sessionErr
}

// PasswordLogin authenticates a user with an email address and password.
func (s *Service) PasswordLogin(ctx context.Context, rawEmail, password string) (User, TokenPair, *consoleservice.Error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return User{}, TokenPair{}, invalidCredentials()
	}
	row, queryErr := s.queries.GetConsoleUserByEmail(ctx, email)
	if queryErr != nil || row.Status != "active" || !VerifyPassword(row.PasswordHash, password) {
		return User{}, TokenPair{}, invalidCredentials()
	}
	user := userFromEmailRow(row)
	pair, sessionErr := s.sessions.Create(ctx, user.UID)
	return user, pair, sessionErr
}

// EmailCodeLogin authenticates a user with a purpose-bound email challenge.
func (s *Service) EmailCodeLogin(
	ctx context.Context,
	rawEmail, challengeID, code, ip string,
) (User, TokenPair, *consoleservice.Error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return User{}, TokenPair{}, err
	}
	reservation, reserveErr := s.verification.Reserve(ctx, email, PurposeLogin, ip, challengeID, code)
	if reserveErr != nil {
		return User{}, TokenPair{}, reserveErr
	}
	release := true
	defer func() {
		if release {
			_ = s.verification.Release(context.Background(), email, PurposeLogin, reservation)
		}
	}()
	row, queryErr := s.queries.GetConsoleUserByEmail(ctx, email)
	if queryErr != nil || row.Status != "active" {
		release = false
		_ = s.verification.Commit(ctx, email, PurposeLogin, reservation)
		return User{}, TokenPair{}, invalidCredentials()
	}
	user := userFromEmailRow(row)
	release = false
	if commitErr := s.verification.Commit(ctx, email, PurposeLogin, reservation); commitErr != nil {
		s.logger.Warn("email-code login challenge finalization failed", zap.Error(commitErr), zap.String("user_uid", user.UID))
	}
	pair, sessionErr := s.sessions.Create(ctx, user.UID)
	return user, pair, sessionErr
}

// ResetPassword verifies a reset challenge, updates the hash, and revokes sessions.
func (s *Service) ResetPassword(
	ctx context.Context,
	rawEmail, newPassword, challengeID, code, ip string,
) *consoleservice.Error {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return err
	}
	if err := ValidatePassword(newPassword); err != nil {
		err.Param = "new_password"
		return err
	}
	reservation, reserveErr := s.verification.Reserve(ctx, email, PurposePasswordReset, ip, challengeID, code)
	if reserveErr != nil {
		return reserveErr
	}
	release := true
	defer func() {
		if release {
			_ = s.verification.Release(context.Background(), email, PurposePasswordReset, reservation)
		}
	}()
	row, queryErr := s.queries.GetConsoleUserByEmail(ctx, email)
	if errors.Is(queryErr, pgx.ErrNoRows) {
		release = false
		_ = s.verification.Commit(ctx, email, PurposePasswordReset, reservation)
		return nil
	}
	if queryErr != nil {
		return requestUnavailable("read password reset user", queryErr)
	}
	hash, hashErr := HashPassword(newPassword)
	if hashErr != nil {
		return requestUnavailable("hash reset password", hashErr)
	}
	if _, updateErr := s.queries.UpdateConsolePassword(ctx, sqlc.UpdateConsolePasswordParams{PasswordHash: hash, ID: row.ID}); updateErr != nil {
		return requestUnavailable("update console password", updateErr)
	}
	release = false
	userUID := uuidString(row.Uid)
	_ = s.sessions.RevokeUser(ctx, userUID)
	if commitErr := s.verification.Commit(ctx, email, PurposePasswordReset, reservation); commitErr != nil {
		s.logger.Warn("password reset challenge finalization failed", zap.Error(commitErr), zap.String("user_uid", userUID))
	}
	return nil
}

// Refresh rotates a refresh token and issues a new token pair.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, *consoleservice.Error) {
	pair, err := s.sessions.Refresh(ctx, refreshToken)
	if err != nil {
		return TokenPair{}, err
	}
	uid, parseErr := uuid.Parse(pair.UserUID)
	if parseErr != nil {
		_ = s.sessions.RevokeUser(ctx, pair.UserUID)
		return TokenPair{}, &consoleservice.Error{Code: CodeRefreshTokenInvalid, Message: "The refresh token is invalid, expired, or revoked.", Status: 401}
	}
	user, queryErr := s.queries.GetConsoleUserByUID(ctx, pgUUID(uid))
	if queryErr != nil || user.Status != "active" {
		_ = s.sessions.RevokeUser(ctx, pair.UserUID)
		return TokenPair{}, &consoleservice.Error{Code: CodeRefreshTokenInvalid, Message: "The refresh token is invalid, expired, or revoked.", Status: 401}
	}
	return pair, nil
}

// Logout revokes the session identified by a refresh token.
func (s *Service) Logout(ctx context.Context, refreshToken string) *consoleservice.Error {
	return s.sessions.Logout(ctx, refreshToken)
}

// LogoutAll revokes every active session for the access-token subject.
func (s *Service) LogoutAll(ctx context.Context, accessToken string) *consoleservice.Error {
	return s.sessions.LogoutAll(ctx, accessToken)
}

// NormalizeEmail validates and canonicalizes an email address for lookup.
func NormalizeEmail(raw string) (string, *consoleservice.Error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 254 || strings.ContainsAny(email, "\r\n") {
		return "", &consoleservice.Error{Code: CodeInvalidEmail, Message: "The email address is invalid.", Param: "email", Status: 422}
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || !strings.Contains(email, "@") {
		return "", &consoleservice.Error{Code: CodeInvalidEmail, Message: "The email address is invalid.", Param: "email", Status: 422}
	}
	return email, nil
}

func defaultDisplayName(email string) string {
	name, _, _ := strings.Cut(email, "@")
	if name == "" {
		return "User"
	}
	return name
}

func pgUUID(value uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte(value), Valid: true}
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func userFromCreateRow(row sqlc.CreateConsoleUserRow) User {
	return User{UID: uuidString(row.Uid), Email: row.Email, DisplayName: row.DisplayName}
}

func userFromEmailRow(row sqlc.GetConsoleUserByEmailRow) User {
	return User{UID: uuidString(row.Uid), Email: row.Email, DisplayName: row.DisplayName}
}
