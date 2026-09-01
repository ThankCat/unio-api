package auth

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	emailsvc "github.com/ThankCat/unio-gateway/internal/service/email"
)

// User 是 Console 的公开用户视图。UID 序列化为 id，内部自增数据库主键永不暴露。
type User struct {
	UID                string  `json:"id"`
	Email              string  `json:"email"`
	DisplayName        string  `json:"display_name"`
	PasswordConfigured bool    `json:"password_configured"`
	Balance            Balance `json:"balance"`
}

// Principal 是已认证会话对应的内部主体。UserID 只用于服务端查询，不得写入公开 JSON。
type Principal struct {
	UserID int64
	UID    string
}

// Balance 是用户钱包快照。后续币种、冻结明细等字段都加在这里，不摊到 User 上。
type Balance struct {
	Currency  string `json:"currency"`
	Total     string `json:"total"`
	Reserved  string `json:"reserved"`
	Available string `json:"available"`
}

// CodeMailer 同步投递验证码邮件（由 internal/service/email.Mailer 实现）。
type CodeMailer interface {
	SendVerificationCode(ctx context.Context, in emailsvc.VerificationCodeMail) error
}

// Service 编排横跨 PostgreSQL 和 Redis 的 Console 认证流程。
type Service struct {
	queries         *sqlc.Queries
	verification    *VerificationStore
	sessions        *SessionManager
	loginLimiter    *PasswordLoginLimiter
	logger          *zap.Logger
	emailCheckDelay func() time.Duration
	mailer          CodeMailer
}

// WithCodeMailer 注入验证码邮件发送器（bootstrap 装配用）。
func (s *Service) WithCodeMailer(mailer CodeMailer) *Service {
	s.mailer = mailer
	return s
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

// SendChallenge 签发与指定用途绑定的邮箱验证码挑战，并同步发送验证码邮件。
//
// 同步发送路径（Blueprint 2026-09-01 决策）：邮件在本次请求内一次有界提交，不建队列、
// 不自动重试；提交失败向用户返回可重试错误，用户在 60 秒重发窗口后再次触发（新挑战
// 自动作废旧挑战）。测试显式注入固定验证码时，允许在 SMTP 未配置的情况下跳过发送。
func (s *Service) SendChallenge(
	ctx context.Context,
	rawEmail string,
	rawPurpose string,
	ip string,
	locale string,
) (Challenge, *consoleservice.Error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return Challenge{}, err
	}
	purpose, err := ParsePurpose(rawPurpose)
	if err != nil {
		return Challenge{}, err
	}
	return s.issueAndDeliverChallenge(ctx, email, purpose, ip, locale)
}

func (s *Service) issueAndDeliverChallenge(
	ctx context.Context,
	email string,
	purpose Purpose,
	ip string,
	locale string,
) (Challenge, *consoleservice.Error) {
	challenge, code, issueErr := s.verification.Issue(ctx, email, purpose, ip)
	if issueErr != nil {
		return Challenge{}, issueErr
	}

	if s.mailer == nil {
		if s.verification.HasFixedCode() {
			return challenge, nil
		}
		s.logger.Error("verification mail skipped: no mailer wired and no fixed code")
		return Challenge{}, verificationDeliveryUnavailable()
	}
	mailErr := s.mailer.SendVerificationCode(ctx, emailsvc.VerificationCodeMail{
		Recipient: email,
		Kind:      verificationKindForPurpose(purpose),
		Code:      code,
		Locale:    locale,
	})
	if mailErr != nil {
		if errors.Is(mailErr, emailsvc.ErrNotConfigured) && s.verification.HasFixedCode() {
			return challenge, nil
		}
		return Challenge{}, verificationDeliveryUnavailable()
	}
	return challenge, nil
}

// verificationKindForPurpose 把验证码用途映射为邮件发送记录的邮件类型。
func verificationKindForPurpose(purpose Purpose) emailsvc.MessageKind {
	switch purpose {
	case PurposeRegister:
		return emailsvc.KindVerificationRegister
	case PurposeLogin:
		return emailsvc.KindVerificationLogin
	case PurposePasswordReset:
		return emailsvc.KindVerificationPasswordReset
	case PurposePasswordSet:
		return emailsvc.KindVerificationPasswordSet
	default:
		return emailsvc.KindVerificationPasswordChange
	}
}

// Register 验证邮箱挑战、创建用户并建立会话。
func (s *Service) Register(
	ctx context.Context,
	rawEmail string,
	password string,
	challengeID string,
	code string,
	ip string,
	userAgent string,
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
		PasswordHash: pgText(hash),
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
	user.Balance = zeroUSDBalance()
	release = false
	if commitChallengeErr := s.verification.Commit(ctx, email, PurposeRegister, reservation); commitChallengeErr != nil {
		s.logger.Warn("registration committed but challenge finalization failed", zap.Error(commitChallengeErr), zap.String("user_uid", user.UID))
	}
	pair, sessionErr := s.sessions.Create(ctx, user.UID, SessionMeta{IP: ip, UserAgent: userAgent})
	return user, pair, sessionErr
}

// PasswordLogin 使用邮箱和密码认证用户。
func (s *Service) PasswordLogin(ctx context.Context, rawEmail, password, ip, userAgent string) (User, TokenPair, *consoleservice.Error) {
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
	if errors.Is(queryErr, pgx.ErrNoRows) || row.Status != "active" || !row.PasswordHash.Valid || !VerifyPassword(row.PasswordHash.String, password) {
		if limitErr := s.loginLimiter.RecordFailure(ctx, email, ip); limitErr != nil {
			return User{}, TokenPair{}, limitErr
		}
		return User{}, TokenPair{}, invalidCredentials()
	}
	if limitErr := s.loginLimiter.ResetEmailIP(ctx, email, ip); limitErr != nil {
		return User{}, TokenPair{}, limitErr
	}
	user := userFromEmailRow(row)
	user, walletErr := s.loadUSDWallet(ctx, user, row.ID)
	if walletErr != nil {
		return User{}, TokenPair{}, walletErr
	}
	pair, sessionErr := s.sessions.Create(ctx, user.UID, SessionMeta{IP: ip, UserAgent: userAgent})
	return user, pair, sessionErr
}

// AuthenticatePrincipal 校验访问令牌并返回内部用户主键，不加载钱包。
func (s *Service) AuthenticatePrincipal(ctx context.Context, accessToken string) (Principal, *consoleservice.Error) {
	row, err := s.lookupActiveUser(ctx, accessToken)
	if err != nil {
		return Principal{}, err
	}
	return Principal{UserID: row.ID, UID: uuidString(row.Uid)}, nil
}

// CurrentUser 返回已认证访问令牌会话对应的活跃用户。
func (s *Service) CurrentUser(ctx context.Context, accessToken string) (User, *consoleservice.Error) {
	row, err := s.lookupActiveUser(ctx, accessToken)
	if err != nil {
		return User{}, err
	}
	return s.loadUSDWallet(ctx, userFromUIDRow(row), row.ID)
}

func (s *Service) lookupActiveUser(ctx context.Context, accessToken string) (sqlc.GetConsoleUserByUIDRow, *consoleservice.Error) {
	userUID, sessionErr := s.sessions.Authenticate(ctx, accessToken)
	if sessionErr != nil {
		return sqlc.GetConsoleUserByUIDRow{}, sessionErr
	}
	uid, parseErr := uuid.Parse(userUID)
	if parseErr != nil {
		return sqlc.GetConsoleUserByUIDRow{}, sessionInvalid(parseErr)
	}
	row, queryErr := s.queries.GetConsoleUserByUID(ctx, pgUUID(uid))
	if errors.Is(queryErr, pgx.ErrNoRows) || (queryErr == nil && row.Status != "active") {
		_ = s.sessions.RevokeUser(ctx, userUID)
		return sqlc.GetConsoleUserByUIDRow{}, sessionInvalid(nil)
	}
	if queryErr != nil {
		return sqlc.GetConsoleUserByUIDRow{}, requestUnavailable("read current console user", queryErr)
	}
	return row, nil
}

// EmailCodeLogin 使用与用途绑定的邮箱挑战认证用户。
func (s *Service) EmailCodeLogin(
	ctx context.Context,
	rawEmail, challengeID, code, ip, userAgent string,
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
	user, walletErr := s.loadUSDWallet(ctx, user, row.ID)
	if walletErr != nil {
		return User{}, TokenPair{}, walletErr
	}
	release = false
	if commitErr := s.verification.Commit(ctx, email, PurposeLogin, reservation); commitErr != nil {
		s.logger.Warn("email-code login challenge finalization failed", zap.Error(commitErr), zap.String("user_uid", user.UID))
	}
	pair, sessionErr := s.sessions.Create(ctx, user.UID, SessionMeta{IP: ip, UserAgent: userAgent})
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
	if _, updateErr := s.queries.UpdateConsolePassword(ctx, sqlc.UpdateConsolePasswordParams{PasswordHash: pgText(hash), ID: row.ID}); updateErr != nil {
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

// 显示名与 Console 前端约定一致：1–32 个中文、ASCII 英文字母或数字。
const maxDisplayNameLength = 32

// ValidateDisplayName 校验展示名原值，不执行清洗、规范化或兜底。
func ValidateDisplayName(name string) *consoleservice.Error {
	count := 0
	for _, r := range name {
		count++
		if !unicode.Is(unicode.Han, r) && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return invalidDisplayName()
		}
	}
	if count == 0 || count > maxDisplayNameLength {
		return invalidDisplayName()
	}
	return nil
}

// UpdateDisplayName 更新当前用户的显示名并返回最新用户视图。
func (s *Service) UpdateDisplayName(ctx context.Context, accessToken, rawName string) (User, *consoleservice.Error) {
	row, err := s.lookupActiveUser(ctx, accessToken)
	if err != nil {
		return User{}, err
	}
	if validateErr := ValidateDisplayName(rawName); validateErr != nil {
		return User{}, validateErr
	}
	updated, updateErr := s.queries.UpdateConsoleDisplayName(ctx, sqlc.UpdateConsoleDisplayNameParams{
		DisplayName: rawName,
		ID:          row.ID,
	})
	if updateErr != nil {
		return User{}, requestUnavailable("update console display name", updateErr)
	}
	user := User{
		UID:                uuidString(updated.Uid),
		Email:              updated.Email,
		DisplayName:        updated.DisplayName,
		PasswordConfigured: updated.PasswordHash.Valid,
	}
	return s.loadUSDWallet(ctx, user, updated.ID)
}

// SessionEntry 是登录会话列表的一项；Current 标记发起本次请求的会话。
type SessionEntry struct {
	SessionInfo
	Current bool
}

// ListSessions 返回当前用户的活跃会话列表。
func (s *Service) ListSessions(ctx context.Context, accessToken string) ([]SessionEntry, *consoleservice.Error) {
	row, err := s.lookupActiveUser(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	currentSID, sidErr := s.sessions.SessionIDFromAccessToken(accessToken)
	if sidErr != nil {
		return nil, sidErr
	}
	sessions, listErr := s.sessions.ListSessions(ctx, uuidString(row.Uid))
	if listErr != nil {
		return nil, listErr
	}
	entries := make([]SessionEntry, 0, len(sessions))
	for _, session := range sessions {
		entries = append(entries, SessionEntry{
			SessionInfo: session,
			Current:     session.SID == currentSID,
		})
	}
	return entries, nil
}

// RevokeSession 注销当前用户名下的一个指定会话。
func (s *Service) RevokeSession(ctx context.Context, accessToken, sid string) *consoleservice.Error {
	row, err := s.lookupActiveUser(ctx, accessToken)
	if err != nil {
		return err
	}
	return s.sessions.RevokeSession(ctx, uuidString(row.Uid), sid)
}

// LogoutOthers 注销当前用户除本会话外的全部会话。
func (s *Service) LogoutOthers(ctx context.Context, accessToken string) *consoleservice.Error {
	row, err := s.lookupActiveUser(ctx, accessToken)
	if err != nil {
		return err
	}
	sid, sidErr := s.sessions.SessionIDFromAccessToken(accessToken)
	if sidErr != nil {
		return sidErr
	}
	return s.sessions.RevokeUserExcept(ctx, uuidString(row.Uid), sid)
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

func pgText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func userFromCreateRow(row sqlc.CreateConsoleUserRow) User {
	return User{UID: uuidString(row.Uid), Email: row.Email, DisplayName: row.DisplayName, PasswordConfigured: row.PasswordHash.Valid}
}

func userFromEmailRow(row sqlc.GetConsoleUserByEmailRow) User {
	return User{UID: uuidString(row.Uid), Email: row.Email, DisplayName: row.DisplayName, PasswordConfigured: row.PasswordHash.Valid}
}

func userFromUIDRow(row sqlc.GetConsoleUserByUIDRow) User {
	return User{UID: uuidString(row.Uid), Email: row.Email, DisplayName: row.DisplayName, PasswordConfigured: row.PasswordHash.Valid}
}
