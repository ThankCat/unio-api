package auth

import serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"

// Authentication request and response DTOs live separately from handler
// control flow so the wire contract remains easy to review and extend.
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

type passwordResetRequest struct {
	Email       string `json:"email"`
	NewPassword string `json:"new_password"`
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
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
