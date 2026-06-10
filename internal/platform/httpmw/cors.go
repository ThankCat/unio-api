package httpmw

import "net/http"

// CORS 放开跨域；dev 阶段允许全部 origin。
// 用 "*" 时不能声明 Allow-Credentials；本服务用 Authorization 头带 token（非 cookie），无需 credentials。
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "300")

		// 预检请求(OPTIONS)直接 204 结束：它不带 Authorization，不能进 AdminAuth，否则会被判 401。
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
