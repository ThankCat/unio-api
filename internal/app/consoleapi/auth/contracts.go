package auth

import serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"

// 认证请求与响应 DTO 独立于处理器控制流，便于审查和扩展传输协议。
type emailCheckRequest struct {
	Email string `json:"email"`
}

type emailCheckData struct {
	Checked bool `json:"checked"`
}

type emailChallengeRequest struct {
	Email   string `json:"email"`
	Purpose string `json:"purpose"`
}

type registrationRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

type passwordSessionRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type emailCodeSessionRequest struct {
	Email       string `json:"email"`
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

type passwordResetVerificationRequest struct {
	Email       string `json:"email"`
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

type passwordResetRequest struct {
	ResetToken  string `json:"reset_token"`
	NewPassword string `json:"new_password"`
}

type updateMeRequest struct {
	DisplayName string `json:"display_name"`
}

type passwordUpdateRequest struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
}

type sessionDTO struct {
	ID         string `json:"id"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  string `json:"created_at,omitempty"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
	Current    bool   `json:"current"`
}

type sessionsData struct {
	Items []sessionDTO `json:"items"`
}

type userData struct {
	User serviceauth.User `json:"user"`
}

type completedData struct {
	Completed bool `json:"completed"`
}

type refreshedData struct {
	Refreshed bool `json:"refreshed"`
}
