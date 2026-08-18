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

// CheckEmail validates login-flow email syntax without looking up an account.
// The fixed delay preserves the endpoint as a timing-safe extension point.
func (s *Service) CheckEmail(ctx context.Context, rawEmail string) *consoleservice.Error {
	if _, err := NormalizeEmail(rawEmail); err != nil {
		return err
	}
	return waitForEmailCheck(ctx, time.NewTimer(s.emailCheckDelay()))
}

// CheckRegistrationEmail reports whether an email can enter registration
// without disclosing why an unavailable address was rejected.
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

// randomEmailCheckDelay returns a uniform delay in the range [1s, 2s).
func randomEmailCheckDelay() time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(emailCheckDelayRange)))
	if err != nil {
		return minimumEmailCheckDelay + emailCheckDelayRange/2
	}
	return minimumEmailCheckDelay + time.Duration(n.Int64())
}
