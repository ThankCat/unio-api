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

// User 是 Console 的公开用户视图。UID 序列化为 id，内部自增数据库主键永不暴露。
type User struct {
	UID         string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// Service 编排横跨 PostgreSQL 和 Redis 的 Console 认证流程。
type Service struct {
	queries         *sqlc.Queries
	verification    *VerificationStore
	sessions        *SessionManager
	loginLimiter    *PasswordLoginLimiter
	logger          *zap.Logger
	emailCheckDelay func() time.Duration
}

// NewService 创建 Console 认证服务。
func NewService(
	db consoleservice.DB,
	verification *VerificationStore,
	sessions *SessionManager,
	loginLimiter *PasswordLoginLimiter,
	logger *zap.Logger,
) (*Service, error) {
	if db == nil || verification == nil || sessions == nil || loginLimiter == nil {
		return nil, errors.New("console authentication dependencies are incomplete")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{
		queries:         sqlc.New(db),
		verification:    verification,
		sessions:        sessions,
		loginLimiter:    loginLimiter,
		logger:          logger,
		emailCheckDelay: randomEmailCheckDelay,
	}, nil
}

// SendChallenge 签发与指定用途绑定的邮箱验证码挑战。
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

// Register 验证邮箱挑战、创建用户并建立会话。
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

// PasswordLogin 使用邮箱和密码认证用户。
func (s *Service) PasswordLogin(ctx context.Context, rawEmail, password, ip string) (User, TokenPair, *consoleservice.Error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return User{}, TokenPair{}, invalidCredentials()
	}
	if limitErr := s.loginLimiter.Check(ctx, email, ip); limitErr != nil {
		return User{}, TokenPair{}, limitErr
	}
	row, queryErr := s.queries.GetConsoleUserByEmail(ctx, email)
	if queryErr != nil && !errors.Is(queryErr, pgx.ErrNoRows) {
		return User{}, TokenPair{}, requestUnavailable("read password login user", queryErr)
	}
	if errors.Is(queryErr, pgx.ErrNoRows) || row.Status != "active" || !VerifyPassword(row.PasswordHash, password) {
		if limitErr := s.loginLimiter.RecordFailure(ctx, email, ip); limitErr != nil {
			return User{}, TokenPair{}, limitErr
		}
		return User{}, TokenPair{}, invalidCredentials()
	}
	if limitErr := s.loginLimiter.ResetEmailIP(ctx, email, ip); limitErr != nil {
		return User{}, TokenPair{}, limitErr
	}
	user := userFromEmailRow(row)
	pair, sessionErr := s.sessions.Create(ctx, user.UID)
	return user, pair, sessionErr
}

// CurrentUser 返回已认证访问令牌会话对应的活跃用户。
func (s *Service) CurrentUser(ctx context.Context, accessToken string) (User, *consoleservice.Error) {
	userUID, sessionErr := s.sessions.Authenticate(ctx, accessToken)
	if sessionErr != nil {
		return User{}, sessionErr
	}
	uid, parseErr := uuid.Parse(userUID)
	if parseErr != nil {
		return User{}, sessionInvalid(parseErr)
	}
	row, queryErr := s.queries.GetConsoleUserByUID(ctx, pgUUID(uid))
	if errors.Is(queryErr, pgx.ErrNoRows) || (queryErr == nil && row.Status != "active") {
		_ = s.sessions.RevokeUser(ctx, userUID)
		return User{}, sessionInvalid(nil)
	}
	if queryErr != nil {
		return User{}, requestUnavailable("read current console user", queryErr)
	}
	return userFromUIDRow(row), nil
}

// EmailCodeLogin 使用与用途绑定的邮箱挑战认证用户。
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

// VerifyPasswordResetCode 消费密码重置挑战，并为密码更新步骤签发一次性凭证。
func (s *Service) VerifyPasswordResetCode(
	ctx context.Context,
	rawEmail, challengeID, code, ip string,
) (PasswordResetGrant, *consoleservice.Error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return PasswordResetGrant{}, err
	}
	reservation, reserveErr := s.verification.Reserve(ctx, email, PurposePasswordReset, ip, challengeID, code)
	if reserveErr != nil {
		return PasswordResetGrant{}, reserveErr
	}
	release := true
	defer func() {
		if release {
			_ = s.verification.Release(context.Background(), email, PurposePasswordReset, reservation)
		}
	}()
	row, queryErr := s.queries.GetConsoleUserByEmail(ctx, email)
	if errors.Is(queryErr, pgx.ErrNoRows) || (queryErr == nil && row.Status != "active") {
		release = false
		_ = s.verification.Commit(ctx, email, PurposePasswordReset, reservation)
		return PasswordResetGrant{}, invalidCredentials()
	}
	if queryErr != nil {
		return PasswordResetGrant{}, requestUnavailable("read password reset user", queryErr)
	}
	grant, grantErr := s.verification.IssuePasswordResetGrant(ctx, email, reservation, uuidString(row.Uid))
	if grantErr != nil {
		return PasswordResetGrant{}, grantErr
	}
	release = false
	return grant, nil
}

// ResetPassword 消费一次性重置凭证、更新密码哈希，并吊销该账户的全部现有会话。
func (s *Service) ResetPassword(ctx context.Context, resetToken, newPassword string) *consoleservice.Error {
	if err := ValidatePassword(newPassword); err != nil {
		err.Param = "new_password"
		return err
	}
	reservation, reserveErr := s.verification.ReservePasswordResetGrant(ctx, resetToken)
	if reserveErr != nil {
		return reserveErr
	}
	release := true
	defer func() {
		if release {
			_ = s.verification.ReleasePasswordResetGrant(context.Background(), reservation)
		}
	}()
	uid, parseErr := uuid.Parse(reservation.UserUID)
	if parseErr != nil {
		release = false
		_ = s.verification.CommitPasswordResetGrant(ctx, reservation)
		return passwordResetTokenUnavailable()
	}
	row, queryErr := s.queries.GetConsoleUserByUID(ctx, pgUUID(uid))
	if errors.Is(queryErr, pgx.ErrNoRows) || (queryErr == nil && row.Status != "active") {
		release = false
		_ = s.verification.CommitPasswordResetGrant(ctx, reservation)
		return passwordResetTokenUnavailable()
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
	userUID := uuidString(row.Uid)
	_ = s.sessions.RevokeUser(ctx, userUID)
	release = false
	if commitErr := s.verification.CommitPasswordResetGrant(ctx, reservation); commitErr != nil {
		s.logger.Warn("password reset credential finalization failed", zap.Error(commitErr), zap.String("user_uid", userUID))
	}
	return nil
}

// Refresh 轮换刷新令牌并签发新令牌对。
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

// Logout 吊销刷新令牌标识的会话。
func (s *Service) Logout(ctx context.Context, refreshToken string) *consoleservice.Error {
	return s.sessions.Logout(ctx, refreshToken)
}

// LogoutAll 吊销访问令牌主体名下的所有活跃会话。
func (s *Service) LogoutAll(ctx context.Context, accessToken string) *consoleservice.Error {
	return s.sessions.LogoutAll(ctx, accessToken)
}

// NormalizeEmail 校验并规范化用于查询的邮箱地址。
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

func userFromUIDRow(row sqlc.GetConsoleUserByUIDRow) User {
	return User{UID: uuidString(row.Uid), Email: row.Email, DisplayName: row.DisplayName}
}
