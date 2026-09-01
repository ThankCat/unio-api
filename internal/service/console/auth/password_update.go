package auth

import (
	"context"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// SendPasswordChallenge 为当前会话账户签发设置或修改密码验证码。
func (s *Service) SendPasswordChallenge(
	ctx context.Context,
	accessToken string,
	ip string,
	locale string,
) (Challenge, *consoleservice.Error) {
	row, err := s.lookupActiveUser(ctx, accessToken)
	if err != nil {
		return Challenge{}, err
	}
	email, emailErr := NormalizeEmail(row.Email)
	if emailErr != nil {
		return Challenge{}, requestUnavailable("normalize password challenge email", emailErr)
	}
	return s.issueAndDeliverChallenge(ctx, email, passwordPurpose(row.PasswordHash.Valid), ip, locale)
}

// UpdatePassword 使用当前账户邮箱验证码设置或修改密码。
func (s *Service) UpdatePassword(
	ctx context.Context,
	accessToken string,
	challengeID string,
	code string,
	newPassword string,
	ip string,
) *consoleservice.Error {
	row, err := s.lookupActiveUser(ctx, accessToken)
	if err != nil {
		return err
	}
	if validateErr := ValidatePassword(newPassword); validateErr != nil {
		validateErr.Param = "new_password"
		return validateErr
	}
	email, emailErr := NormalizeEmail(row.Email)
	if emailErr != nil {
		return requestUnavailable("normalize password update email", emailErr)
	}
	issuedPurpose, purposeErr := s.verification.PasswordPurpose(ctx, email, challengeID)
	if purposeErr != nil {
		return purposeErr
	}
	expectedPurpose := passwordPurpose(row.PasswordHash.Valid)
	if issuedPurpose != expectedPurpose {
		return passwordStateChanged()
	}
	hash, hashErr := HashPassword(newPassword)
	if hashErr != nil {
		return requestUnavailable("hash updated password", hashErr)
	}
	reservation, reserveErr := s.verification.Reserve(ctx, email, issuedPurpose, ip, challengeID, code)
	if reserveErr != nil {
		return reserveErr
	}
	release := true
	defer func() {
		if release {
			_ = s.verification.Release(context.Background(), email, issuedPurpose, reservation)
		}
	}()

	var affected int64
	var updateErr error
	if issuedPurpose == PurposePasswordSet {
		affected, updateErr = s.queries.SetConsolePasswordIfUnset(ctx, sqlc.SetConsolePasswordIfUnsetParams{
			PasswordHash: pgText(hash),
			ID:           row.ID,
		})
	} else {
		affected, updateErr = s.queries.ChangeConsolePasswordIfSet(ctx, sqlc.ChangeConsolePasswordIfSetParams{
			PasswordHash: pgText(hash),
			ID:           row.ID,
		})
	}
	if updateErr != nil {
		return requestUnavailable("update console password", updateErr)
	}
	if affected != 1 {
		release = false
		_ = s.verification.Commit(ctx, email, issuedPurpose, reservation)
		return passwordStateChanged()
	}

	release = false
	if commitErr := s.verification.Commit(ctx, email, issuedPurpose, reservation); commitErr != nil {
		s.logger.Warn("password challenge finalization failed", zap.Error(commitErr), zap.String("user_uid", uuidString(row.Uid)))
	}
	if issuedPurpose == PurposePasswordSet {
		return nil
	}
	sid, sidErr := s.sessions.SessionIDFromAccessToken(accessToken)
	if sidErr != nil {
		return s.sessions.RevokeUser(ctx, uuidString(row.Uid))
	}
	return s.sessions.RevokeUserExcept(ctx, uuidString(row.Uid), sid)
}

func passwordPurpose(configured bool) Purpose {
	if configured {
		return PurposePasswordChange
	}
	return PurposePasswordSet
}
