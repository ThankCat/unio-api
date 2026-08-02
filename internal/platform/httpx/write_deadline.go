package httpx

import (
	"errors"
	"net/http"
	"sync/atomic"
	"time"
)

const DefaultResponseWriteTimeout = 30 * time.Second

var responseWriteTimeoutNanos atomic.Int64

// SetResponseWriteTimeout 设置单次下游写入窗口。服务端可不设绝对 WriteTimeout，实际响应写出仍有边界。
func SetResponseWriteTimeout(timeout time.Duration) {
	if timeout <= 0 {
		responseWriteTimeoutNanos.Store(0)
		return
	}
	responseWriteTimeoutNanos.Store(int64(timeout))
}

func ResponseWriteTimeout() time.Duration {
	if timeout := time.Duration(responseWriteTimeoutNanos.Load()); timeout > 0 {
		return timeout
	}
	return DefaultResponseWriteTimeout
}

// RefreshResponseWriteDeadline 从当前时刻开始刷新一次下游写入窗口。
func RefreshResponseWriteDeadline(w http.ResponseWriter, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = ResponseWriteTimeout()
	}
	return setResponseWriteDeadline(w, time.Now().Add(timeout))
}

// ClearResponseWriteDeadline 清除 server 级绝对写 deadline，供自身具有业务超时的长操作使用。
func ClearResponseWriteDeadline(w http.ResponseWriter) error {
	return setResponseWriteDeadline(w, time.Time{})
}

func setResponseWriteDeadline(w http.ResponseWriter, deadline time.Time) error {
	err := http.NewResponseController(w).SetWriteDeadline(deadline)
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
