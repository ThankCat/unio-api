package responses

import "io"

// readAllLimited 读取 r 的全部内容，但最多 limit 字节，避免异常上游返回超大 body 撑爆内存。
func readAllLimited(r io.Reader, limit int) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, int64(limit)))
}
