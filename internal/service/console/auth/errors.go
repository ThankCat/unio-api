package auth

import consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"

const (
	// CodeInvalidCredentials is returned without revealing which credential failed.
	CodeInvalidCredentials = "auth_invalid_credentials"
	// CodeRefreshTokenInvalid identifies an expired, malformed, or revoked refresh token.
	CodeRefreshTokenInvalid = "auth_refresh_token_invalid"
	// CodeSessionInvalid identifies an invalid authenticated Console session.
	CodeSessionInvalid = "auth_session_invalid"
	// CodeRegistrationUnavailable prevents account-existence disclosure during registration.
	CodeRegistrationUnavailable = "auth_registration_unavailable"
	// CodeVerificationAttemptsExhausted identifies a challenge with no attempts remaining.
	CodeVerificationAttemptsExhausted = "auth_verification_attempts_exhausted"
	// CodeVerificationChallengeUnavailable identifies an expired or unusable challenge.
	CodeVerificationChallengeUnavailable = "auth_verification_challenge_unavailable"
	// CodeInvalidEmail identifies an invalid normalized email address.
	CodeInvalidEmail = "auth_invalid_email"
	// CodeInvalidPurpose identifies an unsupported verification purpose.
	CodeInvalidPurpose = "auth_invalid_purpose"
	// CodeVerificationCodeFormatInvalid identifies a malformed verification code.
	CodeVerificationCodeFormatInvalid = "auth_verification_code_format_invalid"
	// CodeVerificationCodeInvalid identifies an incorrect verification code.
	CodeVerificationCodeInvalid = "auth_verification_code_invalid"
	// CodeVerificationRateLimited identifies a verification rate-limit rejection.
	CodeVerificationRateLimited = "auth_verification_rate_limited"
	// CodeInvalidPassword identifies a password that does not meet the policy.
	CodeInvalidPassword = "auth_invalid_password"
)

func requestUnavailable(operation string, cause error) *consoleservice.Error {
	return consoleservice.RequestUnavailable(operation, cause)
}

func invalidCredentials() *consoleservice.Error {
	return &consoleservice.Error{
		Code:    CodeInvalidCredentials,
		Message: "The email address or password is invalid.",
		Status:  401,
	}
}

func registrationUnavailable() *consoleservice.Error {
	return &consoleservice.Error{
		Code:    CodeRegistrationUnavailable,
		Message: "This email address is unavailable for registration.",
		Param:   "email",
		Status:  409,
	}
}
