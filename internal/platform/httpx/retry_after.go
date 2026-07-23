package httpx

import (
	"net/http"
	"strconv"
	"time"
)

// SetRetryAfter writes a bounded whole-second Retry-After header only when recovery is provable.
func SetRetryAfter(w http.ResponseWriter, duration time.Duration) {
	if w == nil || duration <= 0 {
		return
	}
	seconds := int64((duration + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 300 {
		seconds = 300
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
}
