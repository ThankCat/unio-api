package ticket

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// DefaultAttachmentURLTTL 签名下载 URL 的默认有效期。
// 详情页打开后的一小时内 <img> 都能正常加载；过期后前端重新拉详情即可拿到新签名。
const DefaultAttachmentURLTTL = time.Hour

// AttachmentSigner 为附件下载 URL 生成与校验 HMAC 签名。
// 签名即鉴权：下载端点是公开路由，Console 的 Cookie（跨域 <img> 带不上）和
// Admin 的 Bearer（<img> 根本带不了）都指望不上，所以用短时效签名承载访问控制。
type AttachmentSigner struct {
	secret []byte
}

// NewAttachmentSigner 构建签名器；secret 为空或过短时报错（启动期校验）。
func NewAttachmentSigner(secret string) (*AttachmentSigner, error) {
	if len(secret) < 16 {
		return nil, errors.New("ticket attachment secret must be at least 16 characters")
	}
	return &AttachmentSigner{secret: []byte(secret)}, nil
}

// Sign 计算 uid 在 expiresAt（Unix 秒）前有效的签名。
func (s *AttachmentSigner) Sign(uid uuid.UUID, expiresAt int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(uid.String()))
	mac.Write([]byte("|"))
	mac.Write([]byte(strconv.FormatInt(expiresAt, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify 校验签名与有效期。
func (s *AttachmentSigner) Verify(uid uuid.UUID, expiresAt int64, signature string, now time.Time) bool {
	if expiresAt < now.Unix() {
		return false
	}
	expected := s.Sign(uid, expiresAt)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

// SignedPath 生成相对下载路径（/v1 前缀由各 surface 路由决定，前端再拼各自的 API base）。
func (s *AttachmentSigner) SignedPath(uid uuid.UUID, now time.Time, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = DefaultAttachmentURLTTL
	}
	expiresAt := now.Add(ttl).Unix()
	return fmt.Sprintf(
		"/v1/tickets/attachments/%s?exp=%d&sig=%s",
		uid.String(), expiresAt, url.QueryEscape(s.Sign(uid, expiresAt)),
	)
}
