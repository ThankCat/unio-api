package auth

import consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"

const (
	// CodeInvalidCredentials 表示认证凭据无效，但不会暴露具体失败项。
	CodeInvalidCredentials = "auth_invalid_credentials"
	// CodeRefreshTokenInvalid 表示刷新令牌已过期、格式错误或已被吊销。
	CodeRefreshTokenInvalid = "auth_refresh_token_invalid"
	// CodeSessionInvalid 表示 Console 认证会话无效。
	CodeSessionInvalid = "auth_session_invalid"
	// CodeRegistrationUnavailable 表示当前邮箱无法注册，同时避免暴露账户是否存在。
	CodeRegistrationUnavailable = "auth_registration_unavailable"
	// CodeVerificationAttemptsExhausted 表示验证码挑战已用尽尝试次数。
	CodeVerificationAttemptsExhausted = "auth_verification_attempts_exhausted"
	// CodeVerificationChallengeUnavailable 表示验证码挑战已过期或不可用。
	CodeVerificationChallengeUnavailable = "auth_verification_challenge_unavailable"
	// CodeInvalidEmail 表示规范化后的邮箱地址无效。
	CodeInvalidEmail = "auth_invalid_email"
	// CodeInvalidPurpose 表示验证码用途不受支持。
	CodeInvalidPurpose = "auth_invalid_purpose"
	// CodeVerificationCodeFormatInvalid 表示验证码格式错误。
	CodeVerificationCodeFormatInvalid = "auth_verification_code_format_invalid"
	// CodeVerificationCodeInvalid 表示验证码不正确。
	CodeVerificationCodeInvalid = "auth_verification_code_invalid"
	// CodeVerificationRateLimited 表示验证码请求被限流拒绝。
	CodeVerificationRateLimited = "auth_verification_rate_limited"
	// CodeVerificationDeliveryUnavailable 表示验证码邮件未能发出，可稍后重试。
	CodeVerificationDeliveryUnavailable = "auth_verification_delivery_unavailable"
	// CodePasswordLoginRateLimited 表示密码登录被限流拒绝。
	CodePasswordLoginRateLimited = "auth_password_login_rate_limited"
	// CodeInvalidPassword 表示密码不符合安全策略。
	CodeInvalidPassword = "auth_invalid_password"
	// CodePasswordTooLong 表示密码超过允许长度。
	CodePasswordTooLong = "auth_password_too_long"
	// CodePasswordResetTokenUnavailable 表示密码重置凭证已过期或已使用。
	CodePasswordResetTokenUnavailable = "auth_password_reset_token_unavailable"
	// CodeInvalidDisplayName 表示展示名不符合字符或长度约束。
	CodeInvalidDisplayName = "auth_invalid_display_name"
	// CodePasswordStateChanged 表示发码后账户的密码配置状态已经改变。
	CodePasswordStateChanged = "auth_password_state_changed"
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

func invalidDisplayName() *consoleservice.Error {
	return &consoleservice.Error{
		Code:    CodeInvalidDisplayName,
		Message: "The display name must contain 1-32 Chinese characters, ASCII letters, or digits.",
		Param:   "display_name",
		Status:  422,
	}
}

func passwordStateChanged() *consoleservice.Error {
	return &consoleservice.Error{
		Code:    CodePasswordStateChanged,
		Message: "The password configuration changed after the verification code was sent. Request a new code and try again.",
		Status:  409,
	}
}

func passwordResetTokenUnavailable() *consoleservice.Error {
	return &consoleservice.Error{
		Code:    CodePasswordResetTokenUnavailable,
		Message: "The password reset credential is invalid, expired, or already used.",
		Param:   "reset_token",
		Status:  401,
	}
}

func verificationDeliveryUnavailable() *consoleservice.Error {
	return &consoleservice.Error{
		Code:    CodeVerificationDeliveryUnavailable,
		Message: "The verification email could not be sent. Please try again later.",
		Status:  503,
	}
}

func verificationCodeInvalid(remainingAttempts int) *consoleservice.Error {
	return &consoleservice.Error{
		Code:              CodeVerificationCodeInvalid,
		Message:           "The verification code is incorrect.",
		Param:             "code",
		Status:            422,
		RemainingAttempts: &remainingAttempts,
	}
}
