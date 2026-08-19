package auth

import (
	"context"
	"crypto/rand"
	"math/big"
	"time"

	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

const (
	minimumEmailCheckDelay = time.Second
	emailCheckDelayRange   = time.Second
)

// CheckEmail 检查邮箱是否属于可登录账户，同时保持统一响应延迟和不透明凭据错误。
func (s *Service) CheckEmail(ctx context.Context, rawEmail string) *consoleservice.Error {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return err
	}
	timer := time.NewTimer(s.emailCheckDelay())
	exists, queryErr := s.queries.ConsoleActiveEmailExists(ctx, email)
	if waitErr := waitForEmailCheck(ctx, timer); waitErr != nil {
		return waitErr
	}
	if queryErr != nil {
		return requestUnavailable("check login email availability", queryErr)
	}
	if !exists {
		return invalidCredentials()
	}
	return nil
}

// CheckRegistrationEmail 检查邮箱能否进入注册流程，但不暴露地址被拒绝的原因。
func (s *Service) CheckRegistrationEmail(ctx context.Context, rawEmail string) *consoleservice.Error {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return err
	}
	timer := time.NewTimer(s.emailCheckDelay())
	exists, queryErr := s.queries.ConsoleRegistrationEmailExists(ctx, email)
	if waitErr := waitForEmailCheck(ctx, timer); waitErr != nil {
		return waitErr
	}
	if queryErr != nil {
		return requestUnavailable("check registration email availability", queryErr)
	}
	if exists {
		return registrationUnavailable()
	}
	return nil
}

func waitForEmailCheck(ctx context.Context, timer *time.Timer) *consoleservice.Error {
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return consoleservice.RequestUnavailable("wait for email check", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// randomEmailCheckDelay 返回 [1s, 2s) 范围内的均匀随机延迟。
func randomEmailCheckDelay() time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(emailCheckDelayRange)))
	if err != nil {
		return minimumEmailCheckDelay + emailCheckDelayRange/2
	}
	return minimumEmailCheckDelay + time.Duration(n.Int64())
}
