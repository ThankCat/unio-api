package auth

import (
	"net/http"
	"time"

	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
)

const (
	accessCookieName  = "unio_access_token"
	refreshCookieName = "unio_refresh_token"
)

// writeTokenCookies 在登录或刷新后替换浏览器中的两个令牌 Cookie。
func (h *handler) writeTokenCookies(w http.ResponseWriter, pair serviceauth.TokenPair) {
	now := time.Now()
	http.SetCookie(w, &http.Cookie{
		Name: accessCookieName, Value: pair.AccessToken, Path: "/", Domain: h.cookieDomain,
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode,
		Expires: now.Add(pair.AccessTTL), MaxAge: int(pair.AccessTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: pair.RefreshToken, Path: "/v1/auth/sessions", Domain: h.cookieDomain,
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode,
		Expires: now.Add(pair.RefreshTTL), MaxAge: int(pair.RefreshTTL.Seconds()),
	})
}

// clearTokenCookies 使用原始属性使两个浏览器令牌 Cookie 立即过期。
func (h *handler) clearTokenCookies(w http.ResponseWriter) {
	for _, cookie := range []*http.Cookie{
		{Name: accessCookieName, Path: "/"},
		{Name: refreshCookieName, Path: "/v1/auth/sessions"},
	} {
		cookie.Domain = h.cookieDomain
		cookie.HttpOnly = true
		cookie.Secure = h.cookieSecure
		cookie.SameSite = http.SameSiteLaxMode
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0)
		http.SetCookie(w, cookie)
	}
}
