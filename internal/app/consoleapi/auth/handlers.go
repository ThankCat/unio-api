package auth

import (
	"net/http"

	consolemiddleware "github.com/ThankCat/unio-gateway/internal/app/consoleapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
)

func (h *handler) emailCheck(w http.ResponseWriter, r *http.Request) {
	var request emailCheckRequest
	if err := transport.DecodeJSON(w, r, &request); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	if err := h.service.CheckEmail(r.Context(), request.Email); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, emailCheckData{Checked: true})
}

func (h *handler) registrationEmailCheck(w http.ResponseWriter, r *http.Request) {
	var request emailCheckRequest
	if err := transport.DecodeJSON(w, r, &request); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	if err := h.service.CheckRegistrationEmail(r.Context(), request.Email); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, emailCheckData{Checked: true})
}

func (h *handler) emailChallenge(w http.ResponseWriter, r *http.Request) {
	var request emailChallengeRequest
	if err := transport.DecodeJSON(w, r, &request); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	challenge, err := h.service.SendChallenge(
		r.Context(), request.Email, request.Purpose, consolemiddleware.ClientIPFromContext(r.Context()),
	)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	_ = transport.WriteData(w, http.StatusAccepted, challenge)
}

func (h *handler) registration(w http.ResponseWriter, r *http.Request) {
	var request registrationRequest
	if err := transport.DecodeJSON(w, r, &request); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	user, pair, err := h.service.Register(
		r.Context(), request.Email, request.Password, request.ChallengeID, request.Code,
		consolemiddleware.ClientIPFromContext(r.Context()),
	)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	h.writeTokenCookies(w, pair)
	_ = transport.WriteData(w, http.StatusCreated, userData{User: user})
}

func (h *handler) passwordSession(w http.ResponseWriter, r *http.Request) {
	var request passwordSessionRequest
	if err := transport.DecodeJSON(w, r, &request); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	user, pair, err := h.service.PasswordLogin(
		r.Context(), request.Email, request.Password,
		consolemiddleware.ClientIPFromContext(r.Context()),
	)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	h.writeTokenCookies(w, pair)
	_ = transport.WriteData(w, http.StatusOK, userData{User: user})
}

func (h *handler) currentUser(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(accessCookieName)
	if err != nil || cookie.Value == "" {
		h.errorWriter.Write(w, r, &consoleservice.Error{
			Code:    serviceauth.CodeSessionInvalid,
			Message: "The current session is invalid.",
			Status:  http.StatusUnauthorized,
		})
		return
	}
	user, currentErr := h.service.CurrentUser(r.Context(), cookie.Value)
	if currentErr != nil {
		h.errorWriter.Write(w, r, currentErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, userData{User: user})
}

func (h *handler) emailCodeSession(w http.ResponseWriter, r *http.Request) {
	var request emailCodeSessionRequest
	if err := transport.DecodeJSON(w, r, &request); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	user, pair, err := h.service.EmailCodeLogin(
		r.Context(), request.Email, request.ChallengeID, request.Code,
		consolemiddleware.ClientIPFromContext(r.Context()),
	)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	h.writeTokenCookies(w, pair)
	_ = transport.WriteData(w, http.StatusOK, userData{User: user})
}

func (h *handler) passwordResetVerification(w http.ResponseWriter, r *http.Request) {
	var request passwordResetVerificationRequest
	if err := transport.DecodeJSON(w, r, &request); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	grant, err := h.service.VerifyPasswordResetCode(
		r.Context(), request.Email, request.ChallengeID, request.Code,
		consolemiddleware.ClientIPFromContext(r.Context()),
	)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, grant)
}

func (h *handler) passwordReset(w http.ResponseWriter, r *http.Request) {
	var request passwordResetRequest
	if err := transport.DecodeJSON(w, r, &request); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	if err := h.service.ResetPassword(r.Context(), request.ResetToken, request.NewPassword); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	h.clearTokenCookies(w)
	_ = transport.WriteData(w, http.StatusOK, completedData{Completed: true})
}

func (h *handler) refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		h.errorWriter.Write(w, r, &consoleservice.Error{
			Code:    serviceauth.CodeRefreshTokenInvalid,
			Message: "The refresh token is invalid, expired, or revoked.",
			Status:  http.StatusUnauthorized,
		})
		return
	}
	pair, refreshErr := h.service.Refresh(r.Context(), cookie.Value)
	if refreshErr != nil {
		h.clearTokenCookies(w)
		h.errorWriter.Write(w, r, refreshErr)
		return
	}
	h.writeTokenCookies(w, pair)
	_ = transport.WriteData(w, http.StatusOK, refreshedData{Refreshed: true})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	value := ""
	if cookie, err := r.Cookie(refreshCookieName); err == nil {
		value = cookie.Value
	}
	if err := h.service.Logout(r.Context(), value); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	h.clearTokenCookies(w)
	_ = transport.WriteData(w, http.StatusOK, struct{}{})
}

func (h *handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(accessCookieName)
	if err != nil || cookie.Value == "" {
		h.errorWriter.Write(w, r, &consoleservice.Error{
			Code:    serviceauth.CodeSessionInvalid,
			Message: "The current session is invalid.",
			Status:  http.StatusUnauthorized,
		})
		return
	}
	if logoutErr := h.service.LogoutAll(r.Context(), cookie.Value); logoutErr != nil {
		h.errorWriter.Write(w, r, logoutErr)
		return
	}
	h.clearTokenCookies(w)
	_ = transport.WriteData(w, http.StatusOK, struct{}{})
}
